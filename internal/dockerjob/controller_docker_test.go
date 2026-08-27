package dockerjob

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/oberthci/oberth/internal/runprogress"
	"github.com/oberthci/oberth/pkg/periapsis"
)

// These tests drive a real Docker daemon. They are skipped unless
// OBERTH_DOCKER_TESTS=1, so the ordinary `go test ./...` on a machine with no
// daemon (or no network to pull an image) stays green.
const goImage = "golang:1.26-alpine@sha256:28d89ee9cc0ff9fec75c82ca201e6bf7fdf9a679d4b7b24dfa04f2bb766bb468"

func requireDocker(t *testing.T) *Controller {
	t.Helper()
	if os.Getenv("OBERTH_DOCKER_TESTS") != "1" {
		t.Skip("set OBERTH_DOCKER_TESTS=1 to run tests that drive the Docker daemon")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not on PATH")
	}
	controller, err := NewController(Config{})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	if err := controller.Available(context.Background()); err != nil {
		t.Skipf("docker daemon unavailable: %v", err)
	}
	return controller
}

func pipeline(t *testing.T, body string) []byte {
	t.Helper()
	return []byte("apiVersion: argoproj.io/v1alpha1\nkind: Workflow\nspec:\n  entrypoint: ci\n  activeDeadlineSeconds: 300\n" + body)
}

func submit(t *testing.T, controller *Controller, name string, source []byte) (Completion, string) {
	t.Helper()
	sourceDir := t.TempDir()
	if err := os.WriteFile(sourceDir+"/marker.txt", []byte("source tree\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	request := Request{
		RunID: name + "-run", Name: name, Repo: "acme/widget", Ref: "refs/heads/main",
		SHA: strings.Repeat("a", 40), Trigger: periapsis.TriggerCI, Source: source, SourceDir: sourceDir,
	}
	if _, err := controller.Create(context.Background(), request); err != nil {
		t.Fatalf("Create: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var log bytes.Buffer
	completion, err := controller.Wait(ctx, name, request.RunID, &log)
	if err != nil {
		t.Fatalf("Wait: %v (log: %s)", err, log.String())
	}
	return completion, log.String()
}

func TestDockerRunGoesGreenAndCapturesLogs(t *testing.T) {
	controller := requireDocker(t)
	completion, log := submit(t, controller, "oberth-it-green", pipeline(t, `  templates:
    - name: ci
      dag:
        tasks:
          - name: first
            template: greet
          - name: second
            template: verify
            dependencies: [first]
    - name: greet
      container:
        image: "`+goImage+`"
        command: ["sh", "-c"]
        args: ["echo GREETING_FROM_STEP_ONE"]
    - name: verify
      container:
        image: "`+goImage+`"
        workingDir: /work/src
        command: ["sh", "-c"]
        args: ["cat marker.txt && echo RUN=$OBERTH_RUN_ID && mkdir -p $OBERTH_ARTIFACTS && echo report > $OBERTH_ARTIFACTS/report.txt"]
`))
	if !completion.Succeeded {
		t.Fatalf("expected a green run, got %+v (log: %s)", completion, log)
	}
	if len(completion.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(completion.Steps))
	}
	for _, step := range completion.Steps {
		if step.Status != runprogress.StepPassed {
			t.Fatalf("step %s/%s: %s", step.Burn, step.Step, step.Status)
		}
	}
	// Logs must be attributed to their burn/step, which is what runlog indexes.
	if !strings.Contains(log, "[first/first] GREETING_FROM_STEP_ONE") {
		t.Fatalf("step one log not attributed: %s", log)
	}
	// The immutable checkout must be visible at /work/src.
	if !strings.Contains(log, "[second/second] source tree") {
		t.Fatalf("source tree not seeded into /work/src: %s", log)
	}
	// Server-owned environment must reach the container.
	if !strings.Contains(log, "RUN=oberth-it-green-run") {
		t.Fatalf("OBERTH_RUN_ID not injected: %s", log)
	}
	if len(completion.Artifacts) == 0 {
		t.Fatal("expected the artifact archive to be collected")
	}
}

func TestDockerRunGoesRedWithStepAttribution(t *testing.T) {
	controller := requireDocker(t)
	completion, log := submit(t, controller, "oberth-it-red", pipeline(t, `  templates:
    - name: ci
      dag:
        tasks:
          - name: build
            template: build-burn
    - name: build-burn
      dag:
        tasks:
          - name: ok
            template: ok
          - name: boom
            template: boom
            dependencies: [ok]
          - name: never
            template: ok
            dependencies: [boom]
    - name: ok
      container:
        image: "`+goImage+`"
        command: ["sh", "-c"]
        args: ["echo fine"]
    - name: boom
      container:
        image: "`+goImage+`"
        command: ["sh", "-c"]
        args: ["echo FAILING_ON_PURPOSE >&2; exit 7"]
`))
	if completion.Succeeded {
		t.Fatalf("expected a red run, got %+v", completion)
	}
	if completion.FailedBurn != "build" || completion.FailedStep != "boom" {
		t.Fatalf("failure attribution: got %s/%s", completion.FailedBurn, completion.FailedStep)
	}
	if !strings.Contains(log, "[build/boom] FAILING_ON_PURPOSE") {
		t.Fatalf("stderr of the failing step not captured: %s", log)
	}
	var failed, skipped int
	for _, step := range completion.Steps {
		switch step.Status {
		case runprogress.StepFailed:
			failed++
			if step.ExitCode != 7 {
				t.Fatalf("exit code: got %d, want 7", step.ExitCode)
			}
		case runprogress.StepSkipped:
			skipped++
		}
	}
	if failed != 1 || skipped != 1 {
		t.Fatalf("expected 1 failed and 1 skipped step, got %d and %d (%+v)", failed, skipped, completion.Steps)
	}
}

// A step that fails then succeeds must consume its retry budget rather than
// failing the run on the first attempt.
func TestDockerRunHonoursRetryStrategy(t *testing.T) {
	controller := requireDocker(t)
	completion, log := submit(t, controller, "oberth-it-retry", pipeline(t, `  templates:
    - name: ci
      dag:
        tasks:
          - name: flaky
            template: flaky
    - name: flaky
      retryStrategy:
        limit: "2"
      container:
        image: "`+goImage+`"
        command: ["sh", "-c"]
        args: ["if [ -f /work/cache/attempted ]; then echo SECOND_ATTEMPT; else touch /work/cache/attempted; echo FIRST_ATTEMPT; exit 1; fi"]
`))
	if !completion.Succeeded {
		t.Fatalf("expected the retry to rescue the run, got %+v (log: %s)", completion, log)
	}
	if !strings.Contains(log, "FIRST_ATTEMPT") || !strings.Contains(log, "SECOND_ATTEMPT") {
		t.Fatalf("both attempts should appear in the log: %s", log)
	}
}

// Cleanup is not optional: a leaked volume is leaked disk on the developer's
// laptop, which is the machine this engine exists to be gentle on.
func TestDockerRunRemovesEverythingItCreated(t *testing.T) {
	controller := requireDocker(t)
	completion, _ := submit(t, controller, "oberth-it-cleanup", pipeline(t, `  templates:
    - name: ci
      dag:
        tasks:
          - name: only
            template: run
    - name: run
      container:
        image: "`+goImage+`"
        command: ["true"]
`))
	if !completion.Succeeded {
		t.Fatalf("expected green, got %+v", completion)
	}
	for _, check := range [][]string{
		{"ps", "--all", "--quiet", "--filter", "label=" + labelJob + "=oberth-it-cleanup"},
		{"volume", "ls", "--quiet", "--filter", "name=oberth-it-cleanup-work"},
		{"network", "ls", "--quiet", "--filter", "name=oberth-it-cleanup-net"},
	} {
		out, err := controller.client.run(context.Background(), check...)
		if err != nil {
			t.Fatalf("docker %v: %v", check, err)
		}
		if strings.TrimSpace(out) != "" {
			t.Fatalf("docker %v left %q behind", check, out)
		}
	}
}
