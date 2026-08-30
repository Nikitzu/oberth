package dockerjob

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oberthci/oberth/internal/runprogress"
	"github.com/oberthci/oberth/pkg/argoworkflow"
	"github.com/oberthci/oberth/pkg/periapsis"
)

// Label keys stamped on every Docker object this engine creates. They are what
// makes a run reconstructable after a server restart, which is the same job
// the Workflow object does for the Argo engine.
const (
	labelJob     = "oberth.ci/job"
	labelRun     = "oberth.ci/run"
	labelBurn    = "oberth.ci/burn"
	labelStep    = "oberth.ci/step"
	labelOrdinal = "oberth.ci/ordinal"
	labelAttempt = "oberth.ci/attempt"
)

// ErrNotTerminal mirrors argojob.ErrNotTerminal: the named job is absent or
// still running, so no result can be reported for it yet.
var ErrNotTerminal = errors.New("dockerjob: job has not reached terminal state")

// DefaultArtifactsLimitBytes bounds one run's collected artifact archive.
const DefaultArtifactsLimitBytes = 256 << 20

type Config struct {
	// Docker is the CLI binary; empty selects "docker".
	Docker string
	// RunnerImagePrefixes is the administrator allowlist handed to admission.
	RunnerImagePrefixes []string
	MaxRunLogBytes      int64
	ArtifactsLimitBytes int64
}

// Request is one submission, shaped like argojob.Request so the adapter layer
// reads the same under either engine.
type Request struct {
	RunID     string
	Name      string
	Repo      string
	Ref       string
	SHA       string
	Trigger   periapsis.Trigger
	Source    []byte
	SourceDir string
}

// StepResult is one executed step in Oberth's vocabulary.
type StepResult struct {
	Burn       string
	Step       string
	Ordinal    int
	Status     runprogress.Status
	ExitCode   int
	StartedAt  *time.Time
	FinishedAt *time.Time
}

// Completion is the terminal outcome of one submitted job.
type Completion struct {
	Name       string
	Succeeded  bool
	Phase      string
	Reason     string
	FailedBurn string
	FailedStep string
	Steps      []StepResult
	// Artifacts is the gzipped tar collected from /work/artifacts, empty when
	// the pipeline wrote none. It travels on the completion because the run
	// volume is destroyed as Wait returns, so there is no later moment to ask.
	Artifacts []byte
}

// Controller runs admitted pipelines as Docker containers.
type Controller struct {
	config Config
	client *docker

	mu   sync.Mutex
	jobs map[string]*job
}

type job struct {
	request Request
	plan    Plan
	cancel  context.CancelFunc
}

func NewController(config Config) (*Controller, error) {
	if config.MaxRunLogBytes <= 0 {
		config.MaxRunLogBytes = DefaultMaxRunLogBytes
	}
	if config.ArtifactsLimitBytes <= 0 {
		config.ArtifactsLimitBytes = DefaultArtifactsLimitBytes
	}
	return &Controller{config: config, client: newDocker(config.Docker), jobs: map[string]*job{}}, nil
}

// Available reports whether the Docker daemon answers.
func (controller *Controller) Available(ctx context.Context) error {
	return controller.client.available(ctx)
}

// Create admits and compiles the pipeline, then records it as ready to run.
//
// Nothing is started here. A pipeline that this engine cannot execute is
// refused now, before the run claims a workspace or writes a log, so a
// repository learns the reason at submission rather than discovering a
// half-executed pipeline.
func (controller *Controller) Create(ctx context.Context, request Request) (string, error) {
	if strings.TrimSpace(request.Name) == "" || strings.TrimSpace(request.RunID) == "" {
		return "", errors.New("dockerjob: job name and run ID are required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	workflow, err := argoworkflow.Decode(request.Source)
	if err != nil {
		return "", fmt.Errorf("dockerjob: read repository pipeline document: %w", err)
	}
	// The gate is unchanged and runs first, exactly as it does on the Argo
	// path. Compile refuses what this engine cannot RUN; Admit refuses what
	// Oberth will not ALLOW, and the second is not weakened by the first.
	if err := argoworkflow.Admit(workflow, argoworkflow.Policy{
		RunnerImagePrefixes: controller.config.RunnerImagePrefixes,
	}); err != nil {
		return "", err
	}
	plan, err := Compile(workflow)
	if err != nil {
		return "", err
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if existing, occupied := controller.jobs[request.Name]; occupied && existing.request.RunID != request.RunID {
		return "", fmt.Errorf("dockerjob: job %s is already in flight for run %s", request.Name, existing.request.RunID)
	}
	controller.jobs[request.Name] = &job{request: request, plan: plan}
	return request.Name, nil
}

// PlanFor exposes a submitted job's compiled plan, so the adapter can report
// what a run will do without compiling the document a second time.
func (controller *Controller) PlanFor(name string) (Plan, bool) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	existing, ok := controller.jobs[name]
	if !ok {
		return Plan{}, false
	}
	return existing.plan, true
}

func (controller *Controller) volumeName(name string) string  { return name + "-work" }
func (controller *Controller) networkName(name string) string { return name + "-net" }

// Wait runs the whole pipeline and streams its logs and progress markers into
// destination, returning when the run reaches a terminal state.
func (controller *Controller) Wait(ctx context.Context, name, runID string, destination io.Writer) (Completion, error) {
	controller.mu.Lock()
	current, ok := controller.jobs[name]
	controller.mu.Unlock()
	if !ok {
		return Completion{}, fmt.Errorf("dockerjob: no submitted job named %s", name)
	}
	if current.request.RunID != runID {
		return Completion{}, fmt.Errorf("dockerjob: job %s belongs to run %s, not %s", name, current.request.RunID, runID)
	}
	defer controller.forget(name, runID)

	runCtx, cancel := context.WithCancel(ctx)
	if current.plan.DeadlineSeconds > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(current.plan.DeadlineSeconds)*time.Second)
	}
	defer cancel()
	controller.mu.Lock()
	current.cancel = cancel
	controller.mu.Unlock()

	budget := newRunBudgetWriter(destination, controller.config.MaxRunLogBytes)
	// Cleanup runs even when the run is cancelled mid-flight, on a context
	// that outlives the cancellation: a leaked volume is a leaked disk.
	defer func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancelCleanup()
		controller.cleanup(cleanupCtx, name)
	}()

	completion, err := controller.execute(runCtx, current, budget)
	completion.Name = name
	return completion, err
}

func (controller *Controller) execute(ctx context.Context, current *job, destination io.Writer) (Completion, error) {
	name := current.request.Name
	completion := Completion{Name: name, Succeeded: false, Phase: "job"}

	if err := controller.provision(ctx, current.request); err != nil {
		completion.Reason = err.Error()
		return completion, err
	}

	seeded := false
	lastContainer := ""
	for _, step := range current.plan.Steps {
		result, container, err := controller.runStep(ctx, current, step, &seeded, destination)
		if container != "" {
			lastContainer = container
		}
		completion.Steps = append(completion.Steps, result)
		if err != nil {
			completion.Reason = err.Error()
			completion.FailedBurn, completion.FailedStep = step.Burn, step.Step
			completion.Phase = step.Burn
			// Record the steps the run never reached as skipped, so the step
			// list describes the pipeline rather than only its history.
			completion.Steps = append(completion.Steps, skippedAfter(current.plan.Steps, step.Ordinal)...)
			completion.Artifacts = controller.collect(ctx, lastContainer, destination)
			return completion, nil
		}
	}
	completion.Succeeded = true
	completion.Phase = "passed"
	completion.Artifacts = controller.collect(ctx, lastContainer, destination)
	return completion, nil
}

func skippedAfter(steps []Step, ordinal int) []StepResult {
	var skipped []StepResult
	for _, step := range steps {
		if step.Ordinal <= ordinal {
			continue
		}
		skipped = append(skipped, StepResult{
			Burn: step.Burn, Step: step.Step, Ordinal: step.Ordinal, Status: runprogress.StepSkipped,
		})
	}
	return skipped
}

// provision creates the per-run volume and network.
//
// The network is a per-run bridge with no special rules. That is not egress
// containment; it is the place containment would go. See the spike report.
func (controller *Controller) provision(ctx context.Context, request Request) error {
	labels := []string{"--label", labelJob + "=" + request.Name, "--label", labelRun + "=" + request.RunID}
	volumeArgs := append([]string{"volume", "create"}, labels...)
	if _, err := controller.client.run(ctx, append(volumeArgs, controller.volumeName(request.Name))...); err != nil {
		return fmt.Errorf("dockerjob: create run volume: %w", err)
	}
	networkArgs := append([]string{"network", "create"}, labels...)
	if _, err := controller.client.run(ctx, append(networkArgs, controller.networkName(request.Name))...); err != nil {
		return fmt.Errorf("dockerjob: create run network: %w", err)
	}
	return nil
}

// runStep executes one step, retrying up to its declared limit, and returns
// the last container it created so artifacts can be read from the volume.
func (controller *Controller) runStep(ctx context.Context, current *job, step Step,
	seeded *bool, destination io.Writer) (StepResult, string, error) {
	result := StepResult{Burn: step.Burn, Step: step.Step, Ordinal: step.Ordinal}
	started := time.Now().UTC()
	result.StartedAt = &started
	emit(destination, StepResult{Burn: step.Burn, Step: step.Step, Ordinal: step.Ordinal,
		Status: runprogress.StepRunning, StartedAt: &started})

	logs := newStepLogWriter(destination, step.Burn, step.Step)
	attempts := step.RetryLimit + 1
	lastContainer := ""
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			logs.note(fmt.Sprintf("oberth: attempt %d of %d after: %v", attempt+1, attempts, lastErr))
		}
		container, err := controller.createContainer(ctx, current.request, step, attempt)
		if err != nil {
			lastErr = err
			break
		}
		lastContainer = container
		if !*seeded {
			if err := controller.seed(ctx, container, current.request.SourceDir); err != nil {
				lastErr = err
				break
			}
			*seeded = true
		}
		exitCode, err := controller.runContainer(ctx, container, logs)
		result.ExitCode = exitCode
		if err == nil && exitCode == 0 {
			_ = logs.Close()
			finished := time.Now().UTC()
			result.Status, result.FinishedAt = runprogress.StepPassed, &finished
			emit(destination, result)
			return result, lastContainer, nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("step %s/%s exited %d", step.Burn, step.Step, exitCode)
		}
		if ctx.Err() != nil {
			break
		}
	}
	_ = logs.Close()
	finished := time.Now().UTC()
	result.FinishedAt = &finished
	result.Status = runprogress.StepFailed
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.Status = runprogress.StepTimedOut
		lastErr = fmt.Errorf("run exceeded activeDeadlineSeconds: %w", lastErr)
	}
	emit(destination, result)
	return result, lastContainer, lastErr
}

func (controller *Controller) createContainer(ctx context.Context, request Request, step Step, attempt int) (string, error) {
	arguments := []string{
		"create",
		"--name", fmt.Sprintf("%s-%d-%d", request.Name, step.Ordinal, attempt),
		"--network", controller.networkName(request.Name),
		"--volume", controller.volumeName(request.Name) + ":" + WorkMountPath,
		"--label", labelJob + "=" + request.Name,
		"--label", labelRun + "=" + request.RunID,
		"--label", labelBurn + "=" + step.Burn,
		"--label", labelStep + "=" + step.Step,
		"--label", labelOrdinal + "=" + strconv.Itoa(step.Ordinal),
		"--label", labelAttempt + "=" + strconv.Itoa(attempt),
	}
	for _, variable := range controller.stepEnvironment(request, step) {
		arguments = append(arguments, "--env", variable)
	}
	if step.WorkingDir != "" {
		arguments = append(arguments, "--workdir", step.WorkingDir)
	}
	arguments = append(arguments, step.Image)
	arguments = append(arguments, step.Command...)
	arguments = append(arguments, step.Args...)
	container, err := controller.client.run(ctx, arguments...)
	if err != nil {
		return "", fmt.Errorf("dockerjob: create step container for %s/%s: %w", step.Burn, step.Step, err)
	}
	return container, nil
}

// stepEnvironment is the server-owned environment every step receives, plus
// the repository's own declarations. Server values come first so a repository
// cannot shadow OBERTH_SHA with its own: docker applies the last --env wins,
// so the order here is deliberate and the opposite would be a hole.
func (controller *Controller) stepEnvironment(request Request, step Step) []string {
	environment := append([]string(nil), step.Env...)
	return append(environment,
		"OBERTH_REPO="+request.Repo,
		"OBERTH_REF="+request.Ref,
		"OBERTH_SHA="+request.SHA,
		"OBERTH_TRIGGER="+string(request.Trigger),
		"OBERTH_RUN_ID="+request.RunID,
		"OBERTH_ARTIFACTS="+ArtifactsMountPath,
		"OBERTH_CACHE_DIR="+CacheMountPath,
		"OBERTH_FILES="+FilesMountPath,
	)
}

// seed lays out the /work tree inside the run volume and copies the immutable
// checkout into /work/src.
//
// It copies into a created-but-not-started container rather than bind-mounting
// the host workspace. A bind mount would put the server's own data directory
// inside a container running unreviewed code from a pushed commit, which is
// the one thing the whole design is about not doing.
func (controller *Controller) seed(ctx context.Context, container, sourceDir string) error {
	if strings.TrimSpace(sourceDir) == "" {
		return errors.New("dockerjob: run workspace path is required to seed the source")
	}
	staging, err := os.MkdirTemp("", "oberth-docker-seed-")
	if err != nil {
		return fmt.Errorf("dockerjob: stage run volume layout: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()
	for _, directory := range []string{"src", "cache", "artifacts", "files"} {
		if err := os.MkdirAll(filepath.Join(staging, directory), 0o777); err != nil {
			return fmt.Errorf("dockerjob: stage run volume layout: %w", err)
		}
	}
	if _, err := controller.client.run(ctx, "cp", staging+"/.", container+":"+WorkMountPath); err != nil {
		return fmt.Errorf("dockerjob: create the run volume layout: %w", err)
	}
	if _, err := controller.client.run(ctx, "cp", sourceDir+"/.", container+":"+SourceMountPath); err != nil {
		return fmt.Errorf("dockerjob: copy the checked-out source into the run volume: %w", err)
	}
	return nil
}

// runContainer starts a container, streams its log, and returns its exit code.
func (controller *Controller) runContainer(ctx context.Context, container string, logs io.Writer) (int, error) {
	if _, err := controller.client.run(ctx, "start", container); err != nil {
		return -1, err
	}
	// docker logs --follow replays from the first byte, so starting the
	// stream after the container is running loses nothing.
	if err := controller.client.stream(ctx, logs, "logs", "--follow", container); err != nil && ctx.Err() == nil {
		// A log stream failure must not decide the run: the exit code below
		// is the verdict. Say so in the log and carry on.
		_, _ = io.WriteString(logs, fmt.Sprintf("oberth: log stream ended early: %v\n", err))
	}
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	raw, err := controller.client.run(ctx, "inspect", "--format", "{{.State.ExitCode}}", container)
	if err != nil {
		return -1, err
	}
	exitCode, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return -1, fmt.Errorf("dockerjob: read exit code of %s: %w", container, err)
	}
	return exitCode, nil
}

// collect streams /work/artifacts out of the run volume as a gzipped tar, the
// same archive shape internal/artifacts already ingests from the Argo engine.
func (controller *Controller) collect(ctx context.Context, container string, destination io.Writer) []byte {
	if container == "" {
		return nil
	}
	archive, err := controller.readArtifacts(ctx, container)
	if err != nil {
		_, _ = io.WriteString(destination, fmt.Sprintf("oberth: artifact collection failed: %v\n", err))
		return nil
	}
	return archive
}

func (controller *Controller) readArtifacts(ctx context.Context, container string) ([]byte, error) {
	limit := controller.config.ArtifactsLimitBytes
	var compressed strings.Builder
	writer := gzip.NewWriter(&compressed)
	copied := int64(0)
	err := controller.client.pipe(ctx, func(stream io.Reader) error {
		written, copyErr := io.Copy(writer, io.LimitReader(stream, limit+1))
		copied = written
		return copyErr
	}, "cp", container+":"+ArtifactsMountPath+"/.", "-")
	if err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	if copied > limit {
		return nil, fmt.Errorf("dockerjob: artifacts exceed the %d byte limit", limit)
	}
	return []byte(compressed.String()), nil
}

// Cancel stops a job's containers and removes everything it created.
func (controller *Controller) Cancel(ctx context.Context, name, runID string) error {
	controller.mu.Lock()
	if existing, ok := controller.jobs[name]; ok {
		if existing.request.RunID != runID {
			controller.mu.Unlock()
			return fmt.Errorf("dockerjob: job %s belongs to a different run", name)
		}
		if existing.cancel != nil {
			existing.cancel()
		}
	}
	controller.mu.Unlock()
	controller.cleanup(ctx, name)
	controller.forget(name, runID)
	return nil
}

// Owns reports whether this engine created the named job.
func (controller *Controller) Owns(ctx context.Context, name string) (bool, error) {
	controller.mu.Lock()
	_, inProcess := controller.jobs[name]
	controller.mu.Unlock()
	if inProcess {
		return true, nil
	}
	containers, err := controller.listContainers(ctx, name)
	if err != nil {
		return false, err
	}
	return len(containers) != 0, nil
}

// TerminalState reconstructs a job's outcome from the containers it left
// behind, so a server that restarted mid-run records what really happened
// instead of fabricating an interruption.
func (controller *Controller) TerminalState(ctx context.Context, name string) (*Completion, error) {
	containers, err := controller.listContainers(ctx, name)
	if err != nil {
		return nil, err
	}
	if len(containers) == 0 {
		return nil, ErrNotTerminal
	}
	for _, container := range containers {
		if container.running {
			return nil, ErrNotTerminal
		}
	}
	sort.Slice(containers, func(left, right int) bool {
		if containers[left].ordinal != containers[right].ordinal {
			return containers[left].ordinal < containers[right].ordinal
		}
		return containers[left].attempt < containers[right].attempt
	})
	completion := &Completion{Name: name, Succeeded: true, Phase: "passed"}
	// Only the last attempt of each ordinal decides that step's verdict.
	final := map[int]containerState{}
	for _, container := range containers {
		final[container.ordinal] = container
	}
	ordinals := make([]int, 0, len(final))
	for ordinal := range final {
		ordinals = append(ordinals, ordinal)
	}
	sort.Ints(ordinals)
	for _, ordinal := range ordinals {
		container := final[ordinal]
		status := runprogress.StepPassed
		if container.exitCode != 0 {
			status = runprogress.StepFailed
		}
		completion.Steps = append(completion.Steps, StepResult{
			Burn: container.burn, Step: container.step, Ordinal: ordinal,
			Status: status, ExitCode: container.exitCode,
		})
		if container.exitCode != 0 && completion.Succeeded {
			completion.Succeeded = false
			completion.Phase = container.burn
			completion.FailedBurn, completion.FailedStep = container.burn, container.step
			completion.Reason = fmt.Sprintf("step %s/%s exited %d", container.burn, container.step, container.exitCode)
		}
	}
	return completion, nil
}

type containerState struct {
	id       string
	burn     string
	step     string
	ordinal  int
	attempt  int
	exitCode int
	running  bool
}

func (controller *Controller) listContainers(ctx context.Context, name string) ([]containerState, error) {
	raw, err := controller.client.run(ctx, "ps", "--all", "--no-trunc",
		"--filter", "label="+labelJob+"="+name, "--format", "{{.ID}}")
	if err != nil {
		return nil, err
	}
	var states []containerState
	for _, id := range strings.Fields(raw) {
		format := "{{.State.ExitCode}}\t{{.State.Running}}\t{{index .Config.Labels \"" + labelBurn + "\"}}\t" +
			"{{index .Config.Labels \"" + labelStep + "\"}}\t{{index .Config.Labels \"" + labelOrdinal + "\"}}\t" +
			"{{index .Config.Labels \"" + labelAttempt + "\"}}"
		line, inspectErr := controller.client.run(ctx, "inspect", "--format", format, id)
		if inspectErr != nil {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 6 {
			continue
		}
		exitCode, _ := strconv.Atoi(strings.TrimSpace(fields[0]))
		ordinal, _ := strconv.Atoi(strings.TrimSpace(fields[4]))
		attempt, _ := strconv.Atoi(strings.TrimSpace(fields[5]))
		states = append(states, containerState{
			id: id, exitCode: exitCode, running: strings.TrimSpace(fields[1]) == "true",
			burn: strings.TrimSpace(fields[2]), step: strings.TrimSpace(fields[3]),
			ordinal: ordinal, attempt: attempt,
		})
	}
	return states, nil
}

// cleanup removes every Docker object this job created. Best effort by design:
// a failure to remove a network must not turn a green run red.
func (controller *Controller) cleanup(ctx context.Context, name string) {
	containers, err := controller.listContainers(ctx, name)
	if err == nil {
		for _, container := range containers {
			_, _ = controller.client.run(ctx, "rm", "--force", "--volumes", container.id)
		}
	}
	_, _ = controller.client.run(ctx, "network", "rm", controller.networkName(name))
	_, _ = controller.client.run(ctx, "volume", "rm", "--force", controller.volumeName(name))
}

func (controller *Controller) forget(name, runID string) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if existing, ok := controller.jobs[name]; ok && existing.request.RunID == runID {
		delete(controller.jobs, name)
	}
}

func terminalStatus(status runprogress.Status) bool {
	switch status {
	case runprogress.StepPassed, runprogress.StepFailed, runprogress.StepSkipped, runprogress.StepTimedOut:
		return true
	default:
		return false
	}
}

// emit writes one progress marker, the same inline record the Argo engine
// writes, so internal/runlog and the dashboard read both engines identically.
func emit(destination io.Writer, step StepResult) {
	event := runprogress.Event{
		Version: runprogress.Version,
		Burn:    step.Burn, Step: step.Step, Ordinal: step.Ordinal,
		Status: step.Status, ExitCode: step.ExitCode,
		StartedAt: step.StartedAt, FinishedAt: step.FinishedAt,
	}
	now := time.Now().UTC()
	if event.Status != runprogress.StepQueued && event.StartedAt == nil {
		event.StartedAt = &now
	}
	if terminalStatus(event.Status) && event.FinishedAt == nil {
		event.FinishedAt = &now
	}
	if event.Status == runprogress.StepQueued {
		event.StartedAt, event.FinishedAt = nil, nil
	}
	if event.Status == runprogress.StepRunning {
		event.FinishedAt = nil
	}
	if event.FinishedAt != nil && event.StartedAt != nil && event.FinishedAt.Before(*event.StartedAt) {
		event.FinishedAt = event.StartedAt
	}
	marker, err := runprogress.MarshalMarker(event)
	if err != nil {
		return
	}
	_, _ = destination.Write(marker)
}
