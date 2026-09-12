package dockerjob

import (
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
