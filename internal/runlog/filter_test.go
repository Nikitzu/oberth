package runlog

import (
	"strconv"
	"strings"
	"testing"
)

func seedRun(t *testing.T, runID string, lines []string) *Store {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file, err := store.Create(runID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BuildIndex(runID); err != nil {
		t.Fatal(err)
	}
	return store
}

func numberedStep(prefix string, count int) []string {
	lines := make([]string, 0, count)
	for i := 1; i <= count; i++ {
		lines = append(lines, prefix+" line "+strconv.Itoa(i))
	}
	return lines
}

func TestZeroFilterReturnsTheSameBytesAsAnUnfilteredRead(t *testing.T) {
	store := seedRun(t, "r-zero", numberedStep("[test/unit]", 12))

	body, meta, err := store.ReadFiltered("r-zero", "test", "unit", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(body), "\n"); got != 12 {
		t.Fatalf("zero filter returned %d lines, want all 12", got)
	}
	if !strings.Contains(string(body), "[test/unit] line 1\n") {
		t.Fatalf("zero filter dropped the step prefix: %q", body)
	}
	if meta.TotalLines != 12 || meta.ReturnedLines != 12 || meta.Truncated {
		t.Fatalf("meta = %#v, want all 12 lines untruncated", meta)
	}
}

func TestOffsetAndLimitPageThroughAStepDeterministically(t *testing.T) {
	store := seedRun(t, "r-page", numberedStep("[test/unit]", 20))

	first, _, err := store.ReadFiltered("r-page", "test", "unit", Filter{Offset: 0, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	second, meta, err := store.ReadFiltered("r-page", "test", "unit", Filter{Offset: 5, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), "line 1\n") || strings.Contains(string(first), "line 6\n") {
		t.Fatalf("first page = %q", first)
	}
	if !strings.Contains(string(second), "line 6\n") || strings.Contains(string(second), "line 5\n") {
		t.Fatalf("second page = %q", second)
	}
	if meta.ReturnedLines != 5 || meta.TotalLines != 20 || !meta.Truncated {
		t.Fatalf("meta = %#v, want 5 of 20 truncated", meta)
	}
}

func TestTailReturnsTheLastLines(t *testing.T) {
	store := seedRun(t, "r-tail", numberedStep("[test/unit]", 20))

	body, meta, err := store.ReadFiltered("r-tail", "test", "unit", Filter{Tail: true, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "line 20\n") || strings.Contains(string(body), "line 17\n") {
		t.Fatalf("tail = %q, want the last three lines", body)
	}
	if meta.ReturnedLines != 3 || meta.TotalLines != 20 {
		t.Fatalf("meta = %#v", meta)
	}
}

func TestMetaReportsTheTrueTotalWhenLimitTruncates(t *testing.T) {
	store := seedRun(t, "r-meta", numberedStep("[test/unit]", 200))

	_, meta, err := store.ReadFiltered("r-meta", "test", "unit", Filter{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if meta.ReturnedLines != 3 {
		t.Fatalf("returned %d lines, want 3", meta.ReturnedLines)
	}
	if meta.TotalLines != 200 {
		t.Fatalf("meta.TotalLines = %d, want the true 200 rather than the returned count", meta.TotalLines)
	}
	if meta.MatchedLines != 200 {
		t.Fatalf("meta.MatchedLines = %d, want 200 when no pattern narrows the step", meta.MatchedLines)
	}
	if !meta.Truncated {
		t.Fatal("meta.Truncated is false while 197 lines were withheld")
	}
}

func TestOffsetBeyondTheStepReturnsNothingWithoutError(t *testing.T) {
	store := seedRun(t, "r-past", numberedStep("[test/unit]", 4))

	body, meta, err := store.ReadFiltered("r-past", "test", "unit", Filter{Offset: 99})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 0 || meta.ReturnedLines != 0 {
		t.Fatalf("body = %q meta = %#v, want an empty page", body, meta)
	}
	if meta.TotalLines != 4 {
		t.Fatalf("meta.TotalLines = %d, want 4", meta.TotalLines)
	}
}

func TestFilterSpansEveryRangeOfAMultiBurnStep(t *testing.T) {
	store := seedRun(t, "r-multi", []string{
		"[build/compile] first",
		"[test/unit] alpha",
		"[build/compile] second",
		"[test/unit] beta",
	})

	body, meta, err := store.ReadFiltered("r-multi", "build", "compile", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "first") || !strings.Contains(string(body), "second") {
		t.Fatalf("body = %q, want both compile ranges", body)
	}
	if strings.Contains(string(body), "alpha") {
		t.Fatalf("body = %q, leaked another step", body)
	}
	if meta.TotalLines != 2 {
		t.Fatalf("meta.TotalLines = %d, want 2 across both ranges", meta.TotalLines)
	}
}

func TestEmptyFilterDoesNotTruncateAStepWithManyShortLines(t *testing.T) {
	lines := make([]string, 0, defaultLineLimit+500)
	for i := 0; i < defaultLineLimit+500; i++ {
		lines = append(lines, "[t/u] x")
	}
	store := seedRun(t, "r-short", lines)

	_, meta, err := store.ReadFiltered("r-short", "t", "u", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if meta.Truncated {
		t.Fatalf("an unfiltered read truncated at the default line limit; meta = %#v", meta)
	}
	if meta.ReturnedLines != meta.TotalLines {
		t.Fatalf("returned %d of %d lines with no filter applied", meta.ReturnedLines, meta.TotalLines)
	}
}
