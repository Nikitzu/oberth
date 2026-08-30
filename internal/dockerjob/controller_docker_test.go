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
//
// The attempt marker lives on the per-run volume, not in /work/cache: the
// cache is now shared across runs of a repository, so a marker there would
// make this test pass on its first execution and pass for the wrong reason on
// every later one.
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
        args: ["if [ -f /work/attempted ]; then echo SECOND_ATTEMPT; else touch /work/attempted; echo FIRST_ATTEMPT; exit 1; fi"]
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

// A run that hits its deadline still produced artifacts, and those are exactly
// the ones an author needs to see. Collection used to inherit the run's own
// dead context and hand back nothing.
func TestDockerCollectsArtifactsFromATimedOutRun(t *testing.T) {
	controller := requireDocker(t)
	source := []byte("apiVersion: argoproj.io/v1alpha1\nkind: Workflow\nspec:\n  entrypoint: ci\n  activeDeadlineSeconds: 5\n" + `  templates:
    - name: ci
      dag:
        tasks:
          - name: slow
            template: slow
    - name: slow
      container:
        image: "` + goImage + `"
        command: ["sh", "-c"]
        args: ["mkdir -p $OBERTH_ARTIFACTS && echo partial > $OBERTH_ARTIFACTS/partial.txt && sleep 120"]
`)
	name := "oberth-it-deadline"
	sourceDir := t.TempDir()
	request := Request{
		RunID: name + "-run", Name: name, Repo: "acme/widget", Ref: "refs/heads/main",
		SHA: strings.Repeat("a", 40), Trigger: periapsis.TriggerCI, Source: source, SourceDir: sourceDir,
	}
	if _, err := controller.Create(context.Background(), request); err != nil {
		t.Fatalf("Create: %v", err)
	}
	var log bytes.Buffer
	completion, err := controller.Wait(context.Background(), name, request.RunID, &log)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if completion.Succeeded {
		t.Fatalf("a run that blew its deadline was reported green: %+v", completion)
	}
	if len(completion.Artifacts) == 0 {
		t.Fatalf("no artifacts collected from a timed-out run (log: %s)", log.String())
	}
}

// Provisioning has to survive an abandoned earlier attempt at the same run:
// docker refuses to create a network that already exists, so a leftover one
// used to stop the retry before it reached a step.
func TestDockerProvisionSurvivesLeftoversFromAnEarlierAttempt(t *testing.T) {
	controller := requireDocker(t)
	ctx := context.Background()
	request := Request{RunID: "oberth-it-leftover-run", Name: "oberth-it-leftover"}
	if err := controller.provision(ctx, request); err != nil {
		t.Fatalf("first provision: %v", err)
	}
	defer controller.cleanup(ctx, request.Name)
	if err := controller.provision(ctx, request); err != nil {
		t.Fatalf("second provision over leftovers: %v", err)
	}
}

// The security baseline, asserted against the daemon rather than the argv.
// Each of these is a property the Argo engine gets from the Pod spec, and each
// one was absent here: steps ran as the image's own user with a writable root
// filesystem and every capability the daemon grants by default.
func TestDockerStepRunsUnderTheSecurityBaseline(t *testing.T) {
	controller := requireDocker(t)
	completion, log := submit(t, controller, "oberth-it-hardening", pipeline(t, `  templates:
    - name: ci
      dag:
        tasks:
          - name: posture
            template: posture
    - name: posture
      container:
        image: "`+goImage+`"
        command: ["sh", "-c"]
        args: ["id -u && (echo denied > /denied 2>/dev/null && echo ROOTFS_WRITABLE || echo ROOTFS_READONLY) && (echo scratch > /tmp/scratch && echo TMP_WRITABLE) && (echo work > /work/probe && echo WORK_WRITABLE)"]
`))
	if !completion.Succeeded {
		t.Fatalf("posture probe failed: %+v (log: %s)", completion, log)
	}
	for _, expected := range []string{"ROOTFS_READONLY", "TMP_WRITABLE", "WORK_WRITABLE"} {
		if !strings.Contains(log, expected) {
			t.Fatalf("expected %s in the log, got: %s", expected, log)
		}
	}
	if strings.Contains(log, "ROOTFS_WRITABLE") {
		t.Fatalf("the root filesystem was writable: %s", log)
	}
}

// The scratch mount has to allow execution. Docker's tmpfs default is noexec,
// which breaks every compiler that writes a binary to TMPDIR and runs it, with
// an error naming neither /tmp nor the flag.
func TestDockerScratchMountAllowsExecution(t *testing.T) {
	controller := requireDocker(t)
	completion, log := submit(t, controller, "oberth-it-tmpexec", pipeline(t, `  templates:
    - name: ci
      dag:
        tasks:
          - name: exec
            template: exec
    - name: exec
      container:
        image: "`+goImage+`"
        command: ["sh", "-c"]
        args: ["printf '#!/bin/sh\\necho SCRATCH_EXECUTED\\n' > /tmp/probe.sh && chmod +x /tmp/probe.sh && /tmp/probe.sh"]
`))
	if !completion.Succeeded || !strings.Contains(log, "SCRATCH_EXECUTED") {
		t.Fatalf("scratch mount did not allow execution: %+v (log: %s)", completion, log)
	}
}

// A step broken by the baseline must say so. The underlying error names
// neither the flag nor the mount.
func TestDockerFailedStepExplainsTheSecurityBaseline(t *testing.T) {
	controller := requireDocker(t)
	completion, log := submit(t, controller, "oberth-it-hardening-hint", pipeline(t, `  templates:
    - name: ci
      dag:
        tasks:
          - name: writes-root
            template: writes-root
    - name: writes-root
      container:
        image: "`+goImage+`"
        command: ["sh", "-c"]
        args: ["echo nope > /etc/oberth-probe"]
`))
	if completion.Succeeded {
		t.Fatalf("a write outside /work and /tmp succeeded: %s", log)
	}
	if !strings.Contains(log, "read-only root filesystem") {
		t.Fatalf("the failure did not explain the security baseline: %s", log)
	}
}

// The seeded tree must be root-owned. docker cp from a host path preserves the
// host uid, and a step runs as root with CAP_DAC_OVERRIDE dropped, so a tree
// owned by the developer's uid is unreadable and unwritable from inside the
// step even though the step is root.
func TestDockerSeedsARootOwnedWorkTree(t *testing.T) {
	controller := requireDocker(t)
	name := "oberth-it-seed-ownership"
	sourceDir := t.TempDir()
	// 0600 on purpose: readable by its owner only, so it can only be read
	// inside the container if the copy re-owned it.
	if err := os.WriteFile(sourceDir+"/private.txt", []byte("SEEDED_CONTENT\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.Mkdir(sourceDir+"/nested", 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(sourceDir+"/nested/tool.sh", []byte("#!/bin/sh\necho NESTED_EXECUTED\n"), 0o700); err != nil {
		t.Fatalf("write nested: %v", err)
	}
	request := Request{
		RunID: name + "-run", Name: name, Repo: "acme/widget", Ref: "refs/heads/main",
		SHA: strings.Repeat("a", 40), Trigger: periapsis.TriggerCI, SourceDir: sourceDir,
		Source: pipeline(t, `  templates:
    - name: ci
      dag:
        tasks:
          - name: read
            template: read
    - name: read
      container:
        image: "`+goImage+`"
        workingDir: /work/src
        command: ["sh", "-c"]
        args: ["cat private.txt && ./nested/tool.sh && touch /work/cache/writable && echo CACHE_WRITABLE && stat -c '%u:%g' /work/src"]
`),
	}
	if _, err := controller.Create(context.Background(), request); err != nil {
		t.Fatalf("Create: %v", err)
	}
	var log bytes.Buffer
	completion, err := controller.Wait(context.Background(), name, request.RunID, &log)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !completion.Succeeded {
		t.Fatalf("the step could not read its own checkout: %s", log.String())
	}
	for _, expected := range []string{"SEEDED_CONTENT", "NESTED_EXECUTED", "CACHE_WRITABLE", "0:0"} {
		if !strings.Contains(log.String(), expected) {
			t.Fatalf("expected %q in the log, got: %s", expected, log.String())
		}
	}
}

// The cache survives the run that wrote it, and is scoped to one repository
// and one tier. Before this /work/cache was a directory inside the per-run
// volume, so every run was a cold run.
func TestDockerCacheIsSharedAcrossRunsOfOneRepository(t *testing.T) {
	controller := requireDocker(t)
	document := pipeline(t, `  templates:
    - name: ci
      dag:
        tasks:
          - name: cache
            template: cache
    - name: cache
      container:
        image: "`+goImage+`"
        command: ["sh", "-c"]
        args: ["if [ -f $OBERTH_CACHE_DIR/warm ]; then echo CACHE_WARM; else echo CACHE_COLD; fi; echo warm > $OBERTH_CACHE_DIR/warm"]
`)
	run := func(name, repo string) string {
		sourceDir := t.TempDir()
		request := Request{
			RunID: name + "-run", Name: name, Repo: repo, Ref: "refs/heads/main",
			SHA: strings.Repeat("a", 40), Trigger: periapsis.TriggerCI, Source: document, SourceDir: sourceDir,
		}
		if _, err := controller.Create(context.Background(), request); err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
		var log bytes.Buffer
		completion, err := controller.Wait(context.Background(), name, request.RunID, &log)
		if err != nil {
			t.Fatalf("Wait %s: %v", name, err)
		}
		if !completion.Succeeded {
			t.Fatalf("%s went red: %s", name, log.String())
		}
		return log.String()
	}
	repo := "acme/cache-probe"
	defer func() {
		_, _ = controller.client.run(context.Background(), "volume", "rm", "--force",
			controller.cacheVolumeName(periapsis.TriggerCI, repo))
		_, _ = controller.client.run(context.Background(), "volume", "rm", "--force",
			controller.cacheVolumeName(periapsis.TriggerCI, "acme/other-repo"))
	}()

	if first := run("oberth-it-cache-1", repo); !strings.Contains(first, "CACHE_COLD") {
		t.Fatalf("the first run for a repository was not cold: %s", first)
	}
	if second := run("oberth-it-cache-2", repo); !strings.Contains(second, "CACHE_WARM") {
		t.Fatalf("the second run for the same repository was not warm: %s", second)
	}
	// A different repository must not see it.
	if other := run("oberth-it-cache-3", "acme/other-repo"); !strings.Contains(other, "CACHE_COLD") {
		t.Fatalf("another repository read this repository's cache: %s", other)
	}
}

// Cleanup removes everything the run created and nothing that outlives it.
func TestDockerCleanupKeepsTheRepositoryCache(t *testing.T) {
	controller := requireDocker(t)
	ctx := context.Background()
	repo := "acme/cleanup-probe"
	request := Request{RunID: "oberth-it-cachekeep-run", Name: "oberth-it-cachekeep", Repo: repo, Trigger: periapsis.TriggerCI}
	if err := controller.provision(ctx, request); err != nil {
		t.Fatalf("provision: %v", err)
	}
	cache := controller.cacheVolumeName(periapsis.TriggerCI, repo)
	defer func() { _, _ = controller.client.run(ctx, "volume", "rm", "--force", cache) }()
	controller.cleanup(ctx, request.Name)
	if _, err := controller.client.run(ctx, "volume", "inspect", cache); err != nil {
		t.Fatalf("cleanup removed the repository cache volume: %v", err)
	}
	if _, err := controller.client.run(ctx, "volume", "inspect", controller.volumeName(request.Name)); err == nil {
		t.Fatalf("cleanup left the per-run volume behind")
	}
}

// The same statement has to reach the run log, because that is where someone
// timing a build against the server will be looking.
func TestDockerRunLogStatesTheExecutionOrder(t *testing.T) {
	controller := requireDocker(t)
	_, log := submit(t, controller, "oberth-it-order", pipeline(t, `  templates:
    - name: ci
      dag:
        tasks:
          - name: only
            template: only
    - name: only
      container:
        image: "`+goImage+`"
        command: ["true"]
`))
	if !strings.Contains(log, "do not run concurrently") {
		t.Fatalf("the run log does not state the execution order: %s", log)
	}
}
