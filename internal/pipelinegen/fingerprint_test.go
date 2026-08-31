package pipelinegen

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeFile(t *testing.T, root, relative, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFingerprintIsStableAcrossRepeatedReads(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, ".github/workflows/build.yml", "name: build\njobs: {}\n")
	writeFile(t, root, "package.json", `{"name":"a","version":"1.0.0","scripts":{"test":"jest"}}`)
	writeFile(t, root, "package-lock.json", "{}")

	first := FingerprintInputs(root)
	second := FingerprintInputs(root)
	if len(DriftedInputs(first, second)) != 0 {
		t.Fatalf("two reads of the same checkout drifted: %v", DriftedInputs(first, second))
	}
	for _, want := range []string{".github/workflows/build.yml", "package.json", "package-lock.json"} {
		if first[want] == "" {
			t.Fatalf("fingerprint is missing %q: %v", want, first)
		}
	}
}

func TestFingerprintIgnoresPackageJSONFieldsTheGeneratorNeverReads(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "package.json", `{"name":"a","version":"1.0.0","scripts":{"test":"jest"}}`)
	before := FingerprintInputs(root)
	writeFile(t, root, "package.json", `{"name":"a","version":"9.9.9","dependencies":{"left-pad":"1"},"scripts":{"test":"jest"}}`)
	after := FingerprintInputs(root)
	if drifted := DriftedInputs(before, after); len(drifted) != 0 {
		t.Fatalf("a version bump reported drift in %v; only scripts, engines and packageManager are inputs", drifted)
	}
}

func TestFingerprintNamesTheChangedWorkflow(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, ".github/workflows/build.yml", "name: build\n")
	writeFile(t, root, ".github/workflows/release.yml", "name: release\n")
	before := FingerprintInputs(root)
	writeFile(t, root, ".github/workflows/build.yml", "name: build\non: push\n")
	after := FingerprintInputs(root)
	if drifted := DriftedInputs(before, after); !slices.Equal(drifted, []string{".github/workflows/build.yml"}) {
		t.Fatalf("drifted = %v, want only the workflow that changed", drifted)
	}
}

func TestFingerprintReportsAnInputThatAppearedOrDisappeared(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example\n")
	before := FingerprintInputs(root)
	if err := os.Remove(filepath.Join(root, "go.mod")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "pnpm-lock.yaml", "lockfileVersion: 9\n")
	after := FingerprintInputs(root)
	if drifted := DriftedInputs(before, after); !slices.Equal(drifted, []string{"go.mod", "pnpm-lock.yaml"}) {
		t.Fatalf("drifted = %v, want both the removed and the added input", drifted)
	}
}

func TestFingerprintIgnoresALockfileDependencyBump(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "pnpm-lock.yaml", "lockfileVersion: 9\npackages: {}\n")
	before := FingerprintInputs(root)
	writeFile(t, root, "pnpm-lock.yaml", "lockfileVersion: 9\npackages:\n  left-pad: {}\n")
	after := FingerprintInputs(root)
	if drifted := DriftedInputs(before, after); len(drifted) != 0 {
		t.Fatalf("a lockfile content change reported drift in %v; the generator reads the name only", drifted)
	}
}
