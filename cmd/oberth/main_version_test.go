package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionReportsInjectedReleaseIdentity(t *testing.T) {
	originalVersion, originalCommit, originalDate := version, commit, date
	t.Cleanup(func() { version, commit, date = originalVersion, originalCommit, originalDate })
	version, commit, date = "v1.2.3", "0123456789ab", "2026-08-06T10:00:00Z"
	var output bytes.Buffer
	if err := runCLI(context.Background(), []string{"version"}, strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "oberth v1.2.3 commit=0123456789ab date=2026-08-06T10:00:00Z\n"; got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
	if err := runCLI(context.Background(), []string{"version", "extra"}, strings.NewReader(""), &output); err == nil {
		t.Fatal("version accepted an argument")
	}
}

// TestVersionLdflagsInjection proves that go build -ldflags -X flags override
// the default version/commit/date values — the same mechanism the Makefile and
// Dockerfile use. This is NOT a Makefile invocation test; it validates the
// linker contract that the Makefile relies on.
func TestVersionLdflagsInjection(t *testing.T) {
	t.Parallel()
	binary := filepath.Join(t.TempDir(), "oberth-ldflags-test")
	wantVersion := "ldflags-test-v42.0.0"
	wantCommit := "deadbeef1234"
	wantDate := "2026-08-17T00:00:00Z"
	ldflags := "-s -w" +
		" -X main.version=" + wantVersion +
		" -X main.commit=" + wantCommit +
		" -X main.date=" + wantDate
	cmd := exec.Command("go", "build", "-ldflags", ldflags, "-o", binary, "./")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	out, err := exec.Command(binary, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("oberth version: %v\n%s", err, out)
	}
	got := string(out)
	for _, want := range []string{wantVersion, wantCommit, wantDate} {
		if !strings.Contains(got, want) {
			t.Errorf("version output %q missing %q", got, want)
		}
	}
}
