package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/oberthci/oberth/internal/dockerjob"
	"github.com/oberthci/oberth/internal/model"
	"github.com/oberthci/oberth/internal/service"
)

type recordingAuditor struct {
	mu      sync.Mutex
	actions []model.AuditActionSpec
}

func (auditor *recordingAuditor) AppendAuditAction(_ context.Context, spec model.AuditActionSpec) (model.AuditAction, error) {
	auditor.mu.Lock()
	defer auditor.mu.Unlock()
	auditor.actions = append(auditor.actions, spec)
	return model.AuditAction{}, nil
}

type stubDockerControl struct {
	submitted []dockerjob.Request
	createErr error
}

func (control *stubDockerControl) Create(_ context.Context, request dockerjob.Request) (string, error) {
	if control.createErr != nil {
		return "", control.createErr
	}
	control.submitted = append(control.submitted, request)
	return request.Name, nil
}

func (control *stubDockerControl) Wait(context.Context, string, string, io.Writer) (dockerjob.Completion, error) {
	return dockerjob.Completion{}, errors.New("not used")
}
func (control *stubDockerControl) Cancel(context.Context, string, string) error { return nil }
func (control *stubDockerControl) TerminalState(context.Context, string) (*dockerjob.Completion, error) {
	return nil, dockerjob.ErrNotTerminal
}
func (control *stubDockerControl) Owns(context.Context, string) (bool, error) { return false, nil }

const testDigest = "@sha256:66bb8d36ae1ddd72199ed235a089904874ca4079ee517936ca3adb80506a75c1"

func pipelineDeclaring(paths ...string) string {
	declaration := ""
	if len(paths) != 0 {
		declaration = "\n    oberth.ci/secret-paths: \"" + strings.Join(paths, ",") + "\""
	}
	document := `apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  annotations:
    oberth.ci/size: "S"` + declaration + `
spec:
  entrypoint: ci
  activeDeadlineSeconds: 600
  templates:
    - name: ci
      dag:
        tasks:
          - name: unit
            template: unit
    - name: unit
      container:
        image: "golang:1.26` + testDigest + `"
        command: ["oberth"]
        args: ["secretstore", "exec", "--dir=/run/oberth-secrets"`
	for _, path := range paths {
		document += `, "--path=` + path + `"`
	}
	document += `, "--", "true"]
`
	return document
}

func sourceWith(t *testing.T, file, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".oberth"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".oberth", file), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return dir
}

func newTestDockerJobs(t *testing.T, control *stubDockerControl, store bool, allowlist []string) *DockerJobs {
	t.Helper()
	jobs, err := NewDockerJobs(control, &recordingAuditor{})
	if err != nil {
		t.Fatalf("NewDockerJobs: %v", err)
	}
	jobs.SetSecretStore(store, allowlist)
	return jobs
}

func ciRequest(sourceDir string) service.JobRequest {
	return service.JobRequest{
		JobName: "oberth-run-1", SourceDir: sourceDir, UpstreamOrg: "acme",
		Repository: model.Repository{Name: "widget"},
		Run:        model.Run{ID: "run-1", Ref: "refs/heads/main", SHA: strings.Repeat("a", 40), Actor: "dev"},
	}
}

func releaseRequest(sourceDir string) service.JobRequest {
	request := ciRequest(sourceDir)
	request.Run.Release = true
	request.Run.Ref = "refs/tags/v1.0.0"
	return request
}

// The release tier used to refuse outright. It runs, and the tier it runs at
// is the durable run's own, so the identity OpenBao sees is the release
// subject rather than the CI one.
func TestReleaseRunReachesTheEngineAtTheReleaseTier(t *testing.T) {
	control := &stubDockerControl{}
	jobs := newTestDockerJobs(t, control, true, []string{"oberth/data/release/signing"})
	source := sourceWith(t, "release.yaml", pipelineDeclaring("oberth/data/release/signing"))
	if err := jobs.CreateRelease(context.Background(), releaseRequest(source)); err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	if len(control.submitted) != 1 {
		t.Fatalf("expected one submission, got %d", len(control.submitted))
	}
	submitted := control.submitted[0]
	if submitted.Trigger != "release" {
		t.Fatalf("trigger: %q", submitted.Trigger)
	}
	if !submitted.Credentialed || len(submitted.SecretPaths) != 1 {
		t.Fatalf("release run was not credentialed: %+v", submitted)
	}
}

// A system-namespace path the operator did not allowlist must be refused, the
// same answer the approval table gives on the Argo path.
func TestReleaseRunRefusesAnUnallowlistedSystemPath(t *testing.T) {
	control := &stubDockerControl{}
	jobs := newTestDockerJobs(t, control, true, nil)
	source := sourceWith(t, "release.yaml", pipelineDeclaring("oberth/data/release/signing"))
	err := jobs.CreateRelease(context.Background(), releaseRequest(source))
	if err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("expected an allowlist refusal, got %v", err)
	}
	if len(control.submitted) != 0 {
		t.Fatal("an unallowlisted release path reached the engine")
	}
}

// A branch push must never reach a release credential, whatever the operator
// allowlisted.
func TestCIRunRefusesASystemNamespacePath(t *testing.T) {
	control := &stubDockerControl{}
	jobs := newTestDockerJobs(t, control, true, []string{"oberth/data/release/signing"})
	source := sourceWith(t, "build.yaml", pipelineDeclaring("oberth/data/release/signing"))
	err := jobs.CreateCI(context.Background(), ciRequest(source))
	if err == nil || !strings.Contains(err.Error(), "system-namespace") {
		t.Fatalf("expected a system-namespace refusal, got %v", err)
	}
	if len(control.submitted) != 0 {
		t.Fatal("a CI run reached a release path")
	}
}

// An upstream-scoped path belonging to another repository is refused
// structurally, without any grant row being consulted.
func TestCIRunRefusesAnotherRepositorysUpstreamPath(t *testing.T) {
	control := &stubDockerControl{}
	jobs := newTestDockerJobs(t, control, true, nil)
	source := sourceWith(t, "build.yaml", pipelineDeclaring("oberth/upstream/other/thing/deploy"))
	if err := jobs.CreateCI(context.Background(), ciRequest(source)); err == nil {
		t.Fatal("a repository declared another org's upstream secret")
	}
	if len(control.submitted) != 0 {
		t.Fatal("the submission was not stopped")
	}
}

func TestCIRunAcceptsItsOwnUpstreamPath(t *testing.T) {
	control := &stubDockerControl{}
	jobs := newTestDockerJobs(t, control, true, nil)
	source := sourceWith(t, "build.yaml", pipelineDeclaring("oberth/upstream/acme/widget/deploy"))
	if err := jobs.CreateCI(context.Background(), ciRequest(source)); err != nil {
		t.Fatalf("CreateCI: %v", err)
	}
	if len(control.submitted) != 1 || !control.submitted[0].Credentialed {
		t.Fatalf("the run was not submitted as credentialed: %+v", control.submitted)
	}
}

// With no store, a credentialed pipeline is refused rather than run without
// the credentials it declared.
func TestCredentialedPipelineIsRefusedWithNoStore(t *testing.T) {
	control := &stubDockerControl{}
	jobs := newTestDockerJobs(t, control, false, nil)
	source := sourceWith(t, "build.yaml", pipelineDeclaring("oberth/upstream/acme/widget/deploy"))
	err := jobs.CreateCI(context.Background(), ciRequest(source))
	if err == nil || !strings.Contains(err.Error(), "secretstore init") {
		t.Fatalf("expected a refusal pointing at the setup command, got %v", err)
	}
}

// An uncredentialed pipeline runs with no store configured at all, on either
// tier, exactly as it does on the Argo engine.
func TestUncredentialedPipelineRunsWithNoStore(t *testing.T) {
	control := &stubDockerControl{}
	jobs := newTestDockerJobs(t, control, false, nil)
	source := sourceWith(t, "release.yaml", pipelineDeclaring())
	if err := jobs.CreateRelease(context.Background(), releaseRequest(source)); err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	if len(control.submitted) != 1 || control.submitted[0].Credentialed {
		t.Fatalf("an uncredentialed run was marked credentialed: %+v", control.submitted)
	}
}
