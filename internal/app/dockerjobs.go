package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	wfv1 "github.com/argoproj/argo-workflows/v4/pkg/apis/workflow/v1alpha1"

	"github.com/oberthci/oberth/internal/artifacts"
	"github.com/oberthci/oberth/internal/dockerjob"
	"github.com/oberthci/oberth/internal/model"
	"github.com/oberthci/oberth/internal/runprogress"
	"github.com/oberthci/oberth/internal/service"
	"github.com/oberthci/oberth/pkg/argoworkflow"
	"github.com/oberthci/oberth/pkg/periapsis"
)

// dockerControl is the engine seam, narrowed exactly as argoControl narrows
// the Argo engine. Two engines, one shape.
type dockerControl interface {
	Create(context.Context, dockerjob.Request) (string, error)
	Wait(context.Context, string, string, io.Writer) (dockerjob.Completion, error)
	Cancel(context.Context, string, string) error
	TerminalState(context.Context, string) (*dockerjob.Completion, error)
	Owns(context.Context, string) (bool, error)
}

// DockerJobs adapts the Docker execution engine to the same control-plane
// contracts the Argo engine satisfies, so the scheduler, store, audit chain,
// publish flow, and dashboard need no knowledge of which engine ran a run.
//
// It is deliberately thinner than ArgoJobs. The Argo adapter carries an intent
// state machine because Workflow creation is a remote call that can be
// cancelled mid-flight; here Create is local, synchronous, and creates nothing
// until Wait runs, so the same guarantees need far less machinery.
type DockerJobs struct {
	controller     dockerControl
	auditor        service.Auditor
	artifacts      ArtifactStore
	artifactLimit  int64
	artifactBudget int64

	// secretStore reports whether the engine can credential a run. When it
	// cannot, a pipeline declaring secret paths is refused at submission
	// rather than run without the credentials it asked for.
	secretStore bool

	mu               sync.Mutex
	runs             map[string]string // job name -> run ID
	artifactFailures map[string]string
}

func NewDockerJobs(controller dockerControl, auditor service.Auditor) (*DockerJobs, error) {
	if controller == nil {
		return nil, errors.New("app: the docker engine requires a controller")
	}
	if auditor == nil {
		// Same reason the Argo engine insists: the submission binding is the
		// only durable record of what a run was allowed to reach.
		return nil, errors.New("app: the docker engine requires the audit chain")
	}
	return &DockerJobs{controller: controller, auditor: auditor, runs: map[string]string{}}, nil
}

// SetSecretStore declares that the engine has secret-store coordinates and can
// therefore run a credentialed pipeline.
func (jobs *DockerJobs) SetSecretStore(configured bool) { jobs.secretStore = configured }

// SetArtifacts wires artifact persistence, mirroring ArgoJobs.SetArtifacts.
func (jobs *DockerJobs) SetArtifacts(store ArtifactStore, limit, budget int64) {
	jobs.artifacts, jobs.artifactLimit, jobs.artifactBudget = store, limit, budget
}

func (jobs *DockerJobs) CreateCI(ctx context.Context, request service.JobRequest) error {
	return jobs.create(ctx, request, periapsis.TriggerCI)
}

// CreateRelease refuses. The release tier's security property is that OpenBao
// itself refuses a CI identity a release credential, because the role binds a
// Kubernetes ServiceAccount name. With no Kubernetes there is no such binding,
// and the docker engine will not pretend to offer one by moving the boundary
// into Oberth's own process and calling it equivalent.
func (jobs *DockerJobs) CreateRelease(context.Context, service.JobRequest) error {
	return errors.New("app: the release tier is not supported by the docker engine; " +
		"its credential separation is enforced by OpenBao's Kubernetes auth binding, which does not exist here")
}

func (jobs *DockerJobs) create(ctx context.Context, request service.JobRequest, trigger periapsis.Trigger) error {
	if strings.TrimSpace(request.JobName) == "" || strings.TrimSpace(request.Run.ID) == "" || request.Repository.Name == "" {
		return errors.New("app: deterministic job name, run, and repository are required")
	}
	if expected, err := runTrigger(request.Run); err != nil || expected != trigger {
		return errors.New("app: durable run trigger does not match the requested job capability")
	}
	source, err := readArgoSource(request.SourceDir, trigger)
	if err != nil {
		return noPipelineError(err, trigger, request.Repository.Name)
	}
	// The declared paths, authorized for this trigger, this org and this
	// repository before anything runs. The engine is told the answer rather
	// than deriving it, so admission and execution cannot disagree about what
	// a run was allowed to reach.
	paths, err := jobs.authorizeSecretPaths(source, request, trigger)
	if err != nil {
		return err
	}
	testedSHA := request.Run.TestedSHA
	if testedSHA == "" {
		testedSHA = request.Run.SHA
	}
	submission := dockerjob.Request{
		RunID: request.Run.ID, Name: request.JobName, Repo: request.Repository.Name,
		Ref: request.Run.Ref, SHA: testedSHA, Trigger: trigger,
		Source: source, SourceDir: request.SourceDir,
		Credentialed: len(paths) > 0, SecretPaths: paths,
	}
	if err := jobs.auditSubmission(ctx, request, submission); err != nil {
		return err
	}
	created, err := jobs.controller.Create(ctx, submission)
	if err != nil {
		return err
	}
	if created != request.JobName {
		_ = jobs.controller.Cancel(ctx, created, request.Run.ID)
		return fmt.Errorf("app: docker engine created job %q, expected %q", created, request.JobName)
	}
	jobs.mu.Lock()
	jobs.runs[request.JobName] = request.Run.ID
	jobs.mu.Unlock()
	return nil
}

// authorizeSecretPaths returns the secret-store paths a run is allowed to
// reach, or refuses.
//
// The rules are the Argo engine's, because they have to be: an upstream-scoped
// path is authorized structurally against the declaring repository's own org
// and name, and a system-namespace path is refused outright on the CI trigger,
// where the identity's own policy could not read it anyway. What the docker
// engine does not have is the approval table, which the Argo path consults
// only for system-namespace paths on the release trigger; the release tier is
// separately unavailable here, so there is no case in which a grant row would
// have been the deciding factor.
func (jobs *DockerJobs) authorizeSecretPaths(source []byte, request service.JobRequest,
	trigger periapsis.Trigger) ([]string, error) {
	workflow, err := argoworkflow.Decode(source)
	if err != nil {
		// Let Create surface the decode error with its own framing.
		return nil, nil
	}
	paths, err := argoworkflow.DeclaredSecretPaths(workflow)
	if err != nil {
		return nil, fmt.Errorf("app: read declared secret-store paths: %w", err)
	}
	if len(paths) == 0 {
		return nil, nil
	}
	if !jobs.secretStore {
		return nil, fmt.Errorf("app: repository %q declares secret-store paths (%s), and this server has no secret store "+
			"configured for the docker engine; run `oberth secretstore init --engine=docker` and set "+
			"--secretstore-address and --secretstore-jwt-signing-key, or remove the secret declarations",
			request.Repository.Name, strings.Join(paths, ", "))
	}
	if err := argoworkflow.AuthorizeSecretPaths(paths, nil, trigger, request.UpstreamOrg, request.Repository.Name); err != nil {
		return nil, err
	}
	return paths, nil
}

// auditSubmission records what this run was allowed to reach, before anything
// runs. The docker engine's binding is thinner than the Argo engine's because
// there is no ServiceAccount and no namespace, and saying so explicitly is the
// point: an auditor reading the chain can tell the two tiers apart.
func (jobs *DockerJobs) auditSubmission(ctx context.Context, request service.JobRequest, submission dockerjob.Request) error {
	actor := strings.TrimSpace(request.Run.Actor)
	if actor == "" {
		return errors.New("app: job submission requires an attributable uplink identity")
	}
	details := map[string]any{
		"repo": request.Repository.Name, "ref": request.Run.Ref,
		"sha": submission.SHA, "object_sha": request.Run.SHA,
		"trigger": string(submission.Trigger), "engine": "docker", "job": submission.Name,
		"declared_secret_paths": strings.Join(submission.SecretPaths, ","),
		// Stated rather than implied: this engine grants no platform identity.
		// What it grants instead is a signed subject, and naming it is the
		// point: an auditor reading the chain can tell the two apart, and can
		// see that the server minted this run's identity rather than a kubelet.
		"service_account": "",
		"run_identity":    dockerRunIdentity(submission),
		"credentialed":    submission.Credentialed,
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("app: encode job submission audit details: %w", err)
	}
	if _, err := jobs.auditor.AppendAuditAction(ctx, model.AuditActionSpec{
		Actor: actor, Action: string(submission.Trigger) + ".docker.submit.binding",
		ResourceType: "run", ResourceID: request.Run.ID, Details: string(encoded),
	}); err != nil {
		return fmt.Errorf("app: persist job submission binding: %w", err)
	}
	return nil
}

func (jobs *DockerJobs) Wait(ctx context.Context, name string, destination io.Writer) (service.JobResult, error) {
	jobs.mu.Lock()
	runID, ok := jobs.runs[name]
	jobs.mu.Unlock()
	if !ok {
		return service.JobResult{}, fmt.Errorf("app: no in-process intent for job %s", name)
	}
	defer jobs.forget(name, runID)
	completion, err := jobs.controller.Wait(ctx, name, runID, destination)
	if err != nil {
		return service.JobResult{}, err
	}
	if reason := jobs.storeArtifacts(context.WithoutCancel(ctx), runID, completion); reason != "" {
		jobs.recordArtifactFailure(context.WithoutCancel(ctx), runID, reason)
	}
	return dockerResult(completion), nil
}

func (jobs *DockerJobs) Delete(ctx context.Context, name, runID string) error {
	if strings.TrimSpace(runID) == "" {
		return errors.New("app: durable run ID is required to delete a job")
	}
	jobs.mu.Lock()
	if current, ok := jobs.runs[name]; ok && current != runID {
		jobs.mu.Unlock()
		return fmt.Errorf("app: job %s belongs to a different in-process run", name)
	}
	jobs.mu.Unlock()
	if err := jobs.controller.Cancel(ctx, name, runID); err != nil {
		return err
	}
	jobs.forget(name, runID)
	return nil
}

func (jobs *DockerJobs) TerminalResult(ctx context.Context, name string) (service.JobResult, error) {
	completion, err := jobs.controller.TerminalState(ctx, name)
	if errors.Is(err, dockerjob.ErrNotTerminal) {
		return service.JobResult{}, service.ErrJobNotTerminal
	}
	if err != nil {
		return service.JobResult{}, err
	}
	if completion == nil {
		return service.JobResult{}, service.ErrJobNotTerminal
	}
	return dockerResult(*completion), nil
}

// Owns reports whether this engine created the named job.
func (jobs *DockerJobs) Owns(ctx context.Context, name string) (bool, error) {
	return jobs.controller.Owns(ctx, name)
}

func (jobs *DockerJobs) forget(name, runID string) {
	jobs.mu.Lock()
	defer jobs.mu.Unlock()
	if current, ok := jobs.runs[name]; ok && current == runID {
		delete(jobs.runs, name)
	}
}

// PipelineSize reads the run's declared scheduler weight from the checked-out
// document, exactly as the Argo engine does. Both engines read the same field
// of the same document, so a run weighs the same under either.
func (jobs *DockerJobs) PipelineSize(_ context.Context, request service.JobRequest) (periapsis.Size, error) {
	workflow, err := jobs.decodeRequest(request)
	if err != nil {
		return "", err
	}
	size, err := argoworkflow.DeclaredSize(workflow)
	if err != nil {
		return "", fmt.Errorf("app: read repository resource contract: %w", err)
	}
	return size, nil
}

// PlannedSteps enumerates the steps the document declares, through the same
// shared walk the Argo engine uses. Using argoworkflow rather than the docker
// compiler's own step list is deliberate: the plan must describe the reviewed
// document, not this engine's reduction of it, so a divergence between the two
// shows up as a plan that does not match the run rather than being hidden.
func (jobs *DockerJobs) PlannedSteps(_ context.Context, request service.JobRequest) ([]runprogress.PlannedStep, error) {
	workflow, err := jobs.decodeRequest(request)
	if err != nil {
		return nil, err
	}
	planned, err := argoworkflow.PlannedSteps(workflow)
	if err != nil {
		return nil, fmt.Errorf("app: enumerate pipeline steps: %w", err)
	}
	steps := make([]runprogress.PlannedStep, 0, len(planned))
	for _, step := range planned {
		steps = append(steps, runprogress.PlannedStep{Burn: step.Burn, Step: step.Step, Ordinal: step.Ordinal})
	}
	return steps, nil
}

// PipelineCredentialed reports whether the document declares secret paths, the
// same single signal the Argo engine keys everything off.
func (jobs *DockerJobs) PipelineCredentialed(_ context.Context, request service.JobRequest) (bool, error) {
	workflow, err := jobs.decodeRequest(request)
	if err != nil {
		return false, err
	}
	paths, err := argoworkflow.DeclaredSecretPaths(workflow)
	if err != nil {
		return false, fmt.Errorf("app: read declared secret-store paths: %w", err)
	}
	return len(paths) > 0, nil
}

// dockerRunIdentity is the subject the run's minted token carries, recorded on
// the submission binding. It is derived, not read back from the token: the
// token itself is a credential and never leaves the engine.
func dockerRunIdentity(submission dockerjob.Request) string {
	if !submission.Credentialed {
		return ""
	}
	return "jwt:" + string(submission.Trigger)
}

func (jobs *DockerJobs) decodeRequest(request service.JobRequest) (*wfv1.Workflow, error) {
	trigger, err := runTrigger(request.Run)
	if err != nil {
		return nil, err
	}
	source, err := readArgoSource(request.SourceDir, trigger)
	if err != nil {
		return nil, noPipelineError(err, trigger, request.Repository.Name)
	}
	workflow, err := argoworkflow.Decode(source)
	if err != nil {
		return nil, fmt.Errorf("app: read repository pipeline document: %w", err)
	}
	return workflow, nil
}

func (jobs *DockerJobs) storeArtifacts(ctx context.Context, runID string, completion dockerjob.Completion) string {
	if jobs.artifacts == nil || len(completion.Artifacts) == 0 {
		return ""
	}
	if err := jobs.artifacts.Extract(runID, bytes.NewReader(completion.Artifacts), jobs.artifactLimit); err != nil {
		return fmt.Sprintf("store: %v", err)
	}
	details := map[string]any{"bytes": len(completion.Artifacts), "stored": true}
	if evicted, err := jobs.artifacts.Evict(jobs.artifactBudget); err == nil && len(evicted) != 0 {
		details["evicted"] = evicted
	}
	jobs.auditArtifacts(ctx, runID, details)
	return ""
}

func (jobs *DockerJobs) auditArtifacts(ctx context.Context, runID string, details map[string]any) {
	encoded, err := json.Marshal(details)
	if err != nil {
		return
	}
	_, _ = jobs.auditor.AppendAuditAction(ctx, model.AuditActionSpec{
		Actor: "oberth", Action: "ci.docker.artifacts",
		ResourceType: "run", ResourceID: runID, Details: string(encoded),
	})
}

func (jobs *DockerJobs) recordArtifactFailure(ctx context.Context, runID, reason string) {
	jobs.mu.Lock()
	if jobs.artifactFailures == nil {
		jobs.artifactFailures = map[string]string{}
	}
	jobs.artifactFailures[runID] = reason
	jobs.mu.Unlock()
	jobs.auditArtifacts(ctx, runID, map[string]any{"stored": false, "reason": reason})
}

func (jobs *DockerJobs) ArtifactFailure(runID string) string {
	jobs.mu.Lock()
	defer jobs.mu.Unlock()
	return jobs.artifactFailures[runID]
}

// dockerResult maps the engine's completion onto the same durable JobResult
// the Argo engine produces, so nothing downstream can tell which engine ran.
func dockerResult(completion dockerjob.Completion) service.JobResult {
	result := service.JobResult{Status: model.RunFailed, Phase: "job"}
	if completion.Succeeded {
		result.Status = model.RunPassed
		result.Phase = "passed"
	} else {
		result.Error = completion.Reason
		if result.Error == "" {
			result.Error = "docker run " + completion.Phase
		}
	}
	result.Steps = make([]model.StepResult, 0, len(completion.Steps))
	for _, step := range completion.Steps {
		result.Steps = append(result.Steps, model.StepResult{
			Burn: step.Burn, Step: step.Step, Ordinal: step.Ordinal,
			Status: model.StepStatus(step.Status), ExitCode: step.ExitCode,
			StartedAt: step.StartedAt, FinishedAt: step.FinishedAt,
		})
	}
	if completion.FailedStep != "" {
		result.FailedBurn, result.FailedStep = completion.FailedBurn, completion.FailedStep
		result.Phase = completion.FailedBurn
	}
	if result.Status == model.RunPassed && result.FailedStep != "" {
		result.Status = model.RunFailed
		result.Phase = result.FailedBurn
		result.Error = "successful run reported a failed step"
	}
	return result
}

var (
	_ service.JobController              = (*DockerJobs)(nil)
	_ service.ReleaseJobController       = (*DockerJobs)(nil)
	_ service.PipelineSizer              = (*DockerJobs)(nil)
	_ service.PipelinePlanner            = (*DockerJobs)(nil)
	_ service.PipelineCredentialDetector = (*DockerJobs)(nil)
	_                                    = artifacts.TierGated
)
