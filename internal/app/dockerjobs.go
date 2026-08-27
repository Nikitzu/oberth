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
	// A credentialed pipeline must refuse cleanly rather than run without the
	// credentials it declared and fail somewhere deep inside a build.
	if err := refuseCredentialedPipeline(source, request.Repository.Name); err != nil {
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

// refuseCredentialedPipeline rejects a pipeline that declares secret paths.
// The secret store is out of scope for the docker engine, and silently running
// a credentialed pipeline without its credentials would produce a confusing
// red run instead of a clear refusal.
func refuseCredentialedPipeline(source []byte, repo string) error {
	workflow, err := argoworkflow.Decode(source)
	if err != nil {
		// Let Create surface the decode error with its own framing.
		return nil
	}
	paths, err := argoworkflow.DeclaredSecretPaths(workflow)
	if err != nil || len(paths) == 0 {
		return nil
	}
	return fmt.Errorf("app: repository %q declares secret-store paths (%s), and the docker engine has no secret store; "+
		"run this pipeline on the Argo engine or remove the secret declarations",
		repo, strings.Join(paths, ", "))
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
		"declared_secret_paths": "",
		// Stated rather than implied: this engine grants no platform identity,
		// so there is no ServiceAccount binding to record.
		"service_account": "",
		"credentialed":    false,
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

// PipelineCredentialed always reports false: a credentialed pipeline is
// refused at submission by this engine, so no run reaching here is one.
func (jobs *DockerJobs) PipelineCredentialed(context.Context, service.JobRequest) (bool, error) {
	return false, nil
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
