package dockerjob

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oberthci/oberth/pkg/periapsis"
)

// One cache volume per server version, in Docker's volume alphabet.
func TestHelperVolumeIsNamedByVersion(t *testing.T) {
	t.Parallel()
	for version, want := range map[string]string{
		"v0.13.31-poc22": "oberth-helper-v0.13.31-poc22",
		"":               "oberth-helper-dev",
		"dev":            "oberth-helper-dev",
		"v1.0.0+build/7": "oberth-helper-v1.0.0-build-7",
	} {
		if got := HelperVolumeName(version); got != want {
			t.Errorf("HelperVolumeName(%q) = %q, want %q", version, got, want)
		}
	}
}

// A release takes the helper from its image. A development build builds it
// from source when it can, and says exactly what is missing when it cannot.
func TestHelperSourceDecision(t *testing.T) {
	t.Parallel()
	source, err := decideHelperSource(HelperConfig{ImageRef: "ghcr.io/acme/oberth@sha256:abc"}, false)
	if err != nil || source != helperFromImage {
		t.Fatalf("release = (%v, %v), want the image", source, err)
	}
	source, err = decideHelperSource(HelperConfig{SourceDir: "/src/oberth"}, true)
	if err != nil || source != helperFromSource {
		t.Fatalf("dev with go and source = (%v, %v), want a source build", source, err)
	}
	_, err = decideHelperSource(HelperConfig{SourceDir: "/src/oberth"}, false)
	if err == nil || !strings.Contains(err.Error(), "no Go toolchain") {
		t.Fatalf("dev without go = %v, want a refusal naming the toolchain", err)
	}
	_, err = decideHelperSource(HelperConfig{}, true)
	if err == nil || !strings.Contains(err.Error(), "--helper-source") {
		t.Fatalf("dev without source = %v, want a refusal naming the flag", err)
	}
}

// The helper rides a read-only mount at the Argo engine's path, on
// credentialed steps only.
func TestCredentialedStepMountsTheHelperReadOnly(t *testing.T) {
	controller := credentialedController(t, &recordingMinter{token: "signed"})
	controller.helperVolume = HelperVolumeName("v1.2.3")
	credentialed := Request{Name: "job", RunID: "run", Repo: "acme/widget", Trigger: periapsis.TriggerCI, Credentialed: true}
	joined := strings.Join(controller.createArguments(credentialed, Step{Image: "node"}, 0), " ")
	if !strings.Contains(joined, "oberth-helper-v1.2.3:"+HelperMountPath+":ro") {
		t.Fatalf("the helper is not mounted read-only at %s: %s", HelperMountPath, joined)
	}
	plain := Request{Name: "job", RunID: "run", Repo: "acme/widget", Trigger: periapsis.TriggerCI}
	if joined := strings.Join(controller.createArguments(plain, Step{Image: "node"}, 0), " "); strings.Contains(joined, HelperMountPath) {
		t.Fatalf("an uncredentialed step got the helper: %s", joined)
	}
}

func fakeDocker(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil { // #nosec G306 -- a test executable.
		t.Fatalf("write fake docker: %v", err)
	}
	return path
}

func TestHelperCacheTrustsOnlyAVolumeWhoseBinaryRuns(t *testing.T) {
	config := HelperConfig{ImageRef: "ghcr.io/acme/oberth@sha256:abc", Version: "v1.2.3"}
	cases := []struct {
		name   string
		script string
		cached bool
	}{
		{"missing volume", `[ "$1" = volume ] && exit 1; exit 0`, false},
		{"empty volume, helper does not run", `[ "$1" = run ] && exit 127; exit 0`, false},
		{"helper runs", `exit 0`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			controller, err := NewController(Config{Docker: fakeDocker(t, tc.script), Helper: config})
			if err != nil {
				t.Fatalf("NewController: %v", err)
			}
			if got := controller.helperCached(context.Background(), config); got != tc.cached {
				t.Fatalf("cached = %v, want %v", got, tc.cached)
			}
		})
	}
	dev, err := NewController(Config{Docker: fakeDocker(t, `exit 0`), Helper: HelperConfig{Version: "dev", SourceDir: "."}})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	if dev.helperCached(context.Background(), dev.config.Helper) {
		t.Fatal("a development helper must be rebuilt, never served from a cache")
	}
}

func TestFailedHelperSeedRemovesTheVolume(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls")
	script := `echo "$@" >> ` + log + `
case "$1" in create) echo cid; exit 0;; cp) exit 1;; esac
exit 0`
	controller, err := NewController(Config{Docker: fakeDocker(t, script), Helper: HelperConfig{Version: "v9"}})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	if err := controller.writeHelperVolume(context.Background(), HelperVolumeName("v9"), "img", []byte("bin")); err == nil {
		t.Fatal("a failed seed must be an error")
	}
	calls, _ := os.ReadFile(log) // #nosec G304 -- the test's own log.
	if !strings.Contains(string(calls), "volume rm --force oberth-helper-v9") {
		t.Fatalf("the half-written volume was left behind:\n%s", calls)
	}
}
