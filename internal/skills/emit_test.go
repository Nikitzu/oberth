package skills

import (
	"strings"
	"testing"
)

func testSkill() Skill {
	return Skill{Name: "demo", Description: "Do a demo thing. Use when demonstrating.", Body: "# Demo\n\nBody line.\n"}
}

func TestEmitSKILLCarriesFrontmatterAndAnUnchangedBody(t *testing.T) {
	t.Parallel()
	got := string(EmitSKILL(testSkill()))
	want := "---\nname: demo\ndescription: Do a demo thing. Use when demonstrating.\n---\n\n# Demo\n\nBody line.\n"
	if got != want {
		t.Fatalf("EmitSKILL =\n%q\nwant\n%q", got, want)
	}
}

func TestEmitAgentsCarriesNoFrontmatter(t *testing.T) {
	t.Parallel()
	got := string(EmitAgents([]Skill{testSkill()}))
	if strings.HasPrefix(got, "---") {
		t.Fatalf("AGENTS.md emission starts with frontmatter:\n%s", got)
	}
	if !strings.Contains(got, "# Demo") || !strings.Contains(got, "Body line.") {
		t.Fatalf("body missing:\n%s", got)
	}
	if !strings.Contains(got, beginMarker) || !strings.Contains(got, endMarker) {
		t.Fatalf("emission is not delimited, so it can never be updated in place:\n%s", got)
	}
}

func TestMergeAppendsWhenNoMarkersArePresent(t *testing.T) {
	t.Parallel()
	existing := "# Our repo\n\nBuild with make.\n"
	got := string(MergeMarked([]byte(existing), EmitAgents([]Skill{testSkill()})))
	if !strings.HasPrefix(got, existing) {
		t.Fatalf("the repository's own content was not preserved at the top:\n%s", got)
	}
	if !strings.Contains(got, "# Demo") {
		t.Fatalf("generated content missing:\n%s", got)
	}
}

func TestMergePreservesContentAroundTheMarkedRegion(t *testing.T) {
	t.Parallel()
	first := MergeMarked([]byte("ABOVE\n"), EmitAgents([]Skill{testSkill()}))
	withBelow := string(first) + "\nBELOW\n"

	updated := Skill{Name: "demo", Description: "Changed description here for the demo.", Body: "# Demo\n\nSecond version.\n"}
	got := string(MergeMarked([]byte(withBelow), EmitAgents([]Skill{updated})))

	if !strings.HasPrefix(got, "ABOVE\n") {
		t.Fatalf("content above the region was lost:\n%s", got)
	}
	if !strings.HasSuffix(strings.TrimRight(got, "\n"), "BELOW") {
		t.Fatalf("content below the region was lost:\n%s", got)
	}
	if strings.Contains(got, "Body line.") {
		t.Fatalf("the old generated region survived, so the file now has two versions:\n%s", got)
	}
	if !strings.Contains(got, "Second version.") {
		t.Fatalf("the new generated region is missing:\n%s", got)
	}
	if strings.Count(got, beginMarker) != 1 {
		t.Fatalf("the region was duplicated rather than replaced:\n%s", got)
	}
}

func TestMergeIsIdempotent(t *testing.T) {
	t.Parallel()
	generated := EmitAgents([]Skill{testSkill()})
	once := MergeMarked([]byte("ABOVE\n"), generated)
	twice := MergeMarked(once, generated)
	if string(once) != string(twice) {
		t.Fatalf("a second install changed the file:\nfirst\n%s\nsecond\n%s", once, twice)
	}
}

func TestMergeIntoAnEmptyFile(t *testing.T) {
	t.Parallel()
	got := string(MergeMarked(nil, EmitAgents([]Skill{testSkill()})))
	if !strings.Contains(got, "# Demo") {
		t.Fatalf("empty file produced:\n%s", got)
	}
	if strings.Count(got, beginMarker) != 1 {
		t.Fatalf("markers = %d:\n%s", strings.Count(got, beginMarker), got)
	}
}

func TestAMarkerLineInsideAFencedBlockDoesNotSplitTheFile(t *testing.T) {
	t.Parallel()
	existing := "# Our repo\n\nHow the markers look:\n\n```\n" + beginMarker + "\n" + endMarker + "\n```\n\nEnd of ours.\n"
	got := string(MergeMarked([]byte(existing), EmitAgents([]Skill{testSkill()})))

	if !strings.Contains(got, "How the markers look:") || !strings.Contains(got, "End of ours.") {
		t.Fatalf("documentation about the markers was eaten:\n%s", got)
	}
	untouched := "```\n" + beginMarker + "\n" + endMarker + "\n```"
	if !strings.Contains(got, untouched) {
		t.Fatalf("generated content was injected inside the repository's own fenced example:\n%s", got)
	}
	if !strings.Contains(got, "# Demo") {
		t.Fatalf("generated content missing:\n%s", got)
	}
}

func TestMergeLeavesAForeignFileByteIdenticalOutsideTheRegion(t *testing.T) {
	t.Parallel()
	existing := "line one\nline two\nline three\n"
	got := string(MergeMarked([]byte(existing), EmitAgents([]Skill{testSkill()})))
	for _, line := range strings.Split(existing, "\n") {
		if line == "" {
			continue
		}
		if !strings.Contains(got, line) {
			t.Fatalf("lost %q from the repository's own file:\n%s", line, got)
		}
	}
}
