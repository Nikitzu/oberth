package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The install failed with "chart oberth matching <tag> not found in
// oberth-charts index" because it looked for a fork's version in a repository
// that only carries upstream's. The chart that matches this binary is the one
// compiled into it.
func TestTheChartTravelsInsideTheBinary(t *testing.T) {
	dir, cleanup, err := extractEmbeddedChart()
	defer cleanup()
	if err != nil {
		t.Fatalf("no chart embedded: %v", err)
	}
	for _, name := range []string{"Chart.yaml", "values.yaml", "values.schema.json"} {
		if _, statErr := os.Stat(filepath.Join(dir, name)); statErr != nil {
			t.Errorf("%s missing from the extracted chart: %v", name, statErr)
		}
	}
	if entries, readErr := os.ReadDir(filepath.Join(dir, "templates")); readErr != nil || len(entries) == 0 {
		t.Errorf("templates missing from the extracted chart: %v", readErr)
	}
}

// helm is given a path, so the extraction has to be gone afterwards rather
// than leaving a chart on disk that outlives the binary that chose it.
func TestExtractedChartIsRemovedByItsCleanup(t *testing.T) {
	dir, cleanup, err := extractEmbeddedChart()
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, statErr := os.Stat(dir); statErr == nil {
		t.Fatalf("%s survived cleanup", dir)
	}
}

// The advisory gate is why this fork exists; an install that shipped upstream's
// default would do the opposite of what it was installed for.
func TestEmbeddedChartDefaultsToTheForkBehaviour(t *testing.T) {
	dir, cleanup, err := extractEmbeddedChart()
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "publishOnGreen: false") {
		t.Error("the embedded chart does not default to the advisory gate")
	}
}
