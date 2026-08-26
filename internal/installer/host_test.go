package installer

import (
	"os"
	"path/filepath"
	"testing"
)

// helm reads a bare name as repo/chart, so a --chart value naming a file in
// the working directory failed with "non-absolute URLs should be in form of
// repo_name/path_to_chart". Nothing in the interface says to write ./ first.
func TestLocalChartPathIsMadeAbsoluteForHelm(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "oberth-0.13.27-poc5.tgz")
	if err := os.WriteFile(archive, []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	got := localChartReference("oberth-0.13.27-poc5.tgz")
	if !filepath.IsAbs(got) {
		t.Fatalf("localChartReference(%q) = %q, want an absolute path", "oberth-0.13.27-poc5.tgz", got)
	}
	if filepath.Base(got) != "oberth-0.13.27-poc5.tgz" {
		t.Fatalf("resolved to the wrong file: %s", got)
	}
}

// A value naming no local file is a repository reference and must reach helm
// untouched, or --chart could never name a published chart again.
func TestRepositoryChartReferenceIsLeftAlone(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, reference := range []string{"oberth-charts/oberth", "/opt/charts/oberth.tgz"} {
		if got := localChartReference(reference); got != reference {
			t.Errorf("localChartReference(%q) = %q, want it unchanged", reference, got)
		}
	}
}
