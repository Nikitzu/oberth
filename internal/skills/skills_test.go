package skills

import (
	"strings"
	"testing"
)

func TestListNamesEverySkillWithADescription(t *testing.T) {
	t.Parallel()
	all := List()
	if len(all) != 4 {
		t.Fatalf("listed %d skills, want 4: %v", len(all), names(all))
	}
	want := map[string]bool{
		"oberth-triage": true, "oberth-pipeline": true,
		"oberth-fragments": true, "oberth-release": true,
	}
	for _, skill := range all {
		if !want[skill.Name] {
			t.Fatalf("unexpected skill %q", skill.Name)
		}
		if strings.TrimSpace(skill.Description) == "" {
			t.Fatalf("%s has no description; a consumer cannot decide when to use it", skill.Name)
		}
		if strings.TrimSpace(skill.Body) == "" {
			t.Fatalf("%s has no body", skill.Name)
		}
	}
}

func names(all []Skill) []string {
	out := make([]string, 0, len(all))
	for _, skill := range all {
		out = append(out, skill.Name)
	}
	return out
}

func TestListIsSortedSoOutputIsStable(t *testing.T) {
	t.Parallel()
	all := List()
	for index := 1; index < len(all); index++ {
		if all[index-1].Name >= all[index].Name {
			t.Fatalf("List is not sorted: %v", names(all))
		}
	}
}

func TestBodyCarriesNoFrontmatter(t *testing.T) {
	t.Parallel()
	for _, skill := range List() {
		if strings.HasPrefix(strings.TrimSpace(skill.Body), "---") {
			t.Fatalf("%s body still carries frontmatter; AGENTS.md would receive YAML it does not understand", skill.Name)
		}
	}
}

func TestGetReturnsOneSkill(t *testing.T) {
	t.Parallel()
	skill, err := Get("oberth-triage")
	if err != nil {
		t.Fatal(err)
	}
	if skill.Name != "oberth-triage" {
		t.Fatalf("Get returned %q", skill.Name)
	}
}

func TestGetRefusesAnythingOutsideTheEmbeddedSet(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"", "unknown", "../../etc/passwd", "oberth-triage/../oberth-release",
		"/absolute", "oberth-triage.md", "OBERTH-TRIAGE", "oberth triage",
	} {
		if skill, err := Get(name); err == nil {
			t.Fatalf("Get(%q) returned %q", name, skill.Name)
		}
	}
}

func TestEverySkillIsShortEnoughToLoadCheaply(t *testing.T) {
	t.Parallel()
	for _, skill := range List() {
		if size := len(skill.Body); size > maxBodyBytes {
			t.Fatalf("%s is %d bytes, over the %d byte budget; a skill that costs what it saves is not worth loading",
				skill.Name, size, maxBodyBytes)
		}
	}
}

func TestDescriptionSaysWhenToUseTheSkillNotOnlyWhatItIs(t *testing.T) {
	t.Parallel()
	for _, skill := range List() {
		if len(skill.Description) < 40 {
			t.Fatalf("%s description is %d characters; a consumer decides from this alone: %q",
				skill.Name, len(skill.Description), skill.Description)
		}
	}
}

func TestEverySkillFileIsTrackedByGit(t *testing.T) {
	t.Parallel()
	entries, err := content.ReadDir("content")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(catalogue) {
		t.Fatalf("%d files embedded but %d skills loaded; a file failed to parse silently",
			len(entries), len(catalogue))
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "oberth-") {
			t.Fatalf("%s is matched by the repository's oberth-* gitignore rule, so a fresh clone "+
				"would not contain it and go:embed would fail", entry.Name())
		}
	}
}
