package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repo(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

func TestInstallAgentsWritesTheRepositoryRootFile(t *testing.T) {
	t.Parallel()
	root := repo(t)
	result, err := Install(InstallOptions{Root: root, Target: TargetAgents})
	if err != nil {
		t.Fatal(err)
	}
	if result.Target != TargetAgents {
		t.Fatalf("target = %q", result.Target)
	}
	body := read(t, filepath.Join(root, "AGENTS.md"))
	for _, skill := range List() {
		if !strings.Contains(body, strings.SplitN(skill.Body, "\n", 2)[0]) {
			t.Fatalf("%s missing from AGENTS.md", skill.Name)
		}
	}
	if strings.HasPrefix(body, "---") {
		t.Fatal("AGENTS.md starts with frontmatter")
	}
}

func TestInstallClaudeWritesOneDirectoryPerSkill(t *testing.T) {
	t.Parallel()
	root := repo(t)
	if _, err := Install(InstallOptions{Root: root, Target: TargetClaude}); err != nil {
		t.Fatal(err)
	}
	for _, skill := range List() {
		path := filepath.Join(root, ".claude", "skills", skill.Name, "SKILL.md")
		body := read(t, path)
		if !strings.HasPrefix(body, "---\nname: "+skill.Name+"\n") {
			t.Fatalf("%s does not start with its own frontmatter:\n%s", path, body[:80])
		}
	}
}

func TestInstallOneSkillWritesOnlyThatSkill(t *testing.T) {
	t.Parallel()
	root := repo(t)
	if _, err := Install(InstallOptions{Root: root, Target: TargetClaude, Only: "oberth-triage"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", "skills", "oberth-triage", "SKILL.md")); err != nil {
		t.Fatalf("requested skill not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", "skills", "oberth-release", "SKILL.md")); err == nil {
		t.Fatal("a skill that was not asked for was written")
	}
}

func TestInstallReplitWritesItsOwnInstructionsFile(t *testing.T) {
	t.Parallel()
	root := repo(t)
	if _, err := Install(InstallOptions{Root: root, Target: TargetReplit}); err != nil {
		t.Fatal(err)
	}
	body := read(t, filepath.Join(root, "custom_instruction", "instructions.md"))
	if !strings.Contains(body, beginMarker) {
		t.Fatalf("not delimited, so it can never be updated in place:\n%s", body[:120])
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	t.Parallel()
	for _, target := range []Target{TargetAgents, TargetClaude, TargetReplit} {
		root := repo(t)
		if _, err := Install(InstallOptions{Root: root, Target: target}); err != nil {
			t.Fatal(err)
		}
		first := snapshot(t, root)
		if _, err := Install(InstallOptions{Root: root, Target: target}); err != nil {
			t.Fatal(err)
		}
		if second := snapshot(t, root); first != second {
			t.Fatalf("%s: a second install changed the tree", target)
		}
	}
}

func snapshot(t *testing.T, root string) string {
	t.Helper()
	var builder strings.Builder
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		relative, _ := filepath.Rel(root, path)
		builder.WriteString(relative + "\n" + read(t, path) + "\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return builder.String()
}

func TestInstallPreservesARepositorysOwnAgentsFile(t *testing.T) {
	t.Parallel()
	root := repo(t)
	own := "# Our repo\n\nRun make test.\n"
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(own), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(InstallOptions{Root: root, Target: TargetAgents}); err != nil {
		t.Fatalf("install into an existing AGENTS.md needed --force: %v", err)
	}
	body := read(t, filepath.Join(root, "AGENTS.md"))
	if !strings.HasPrefix(body, own) {
		t.Fatalf("the repository's own content was lost:\n%s", body)
	}
}

func TestInstallRefusesToOverwriteAForeignSkillFile(t *testing.T) {
	t.Parallel()
	root := repo(t)
	path := filepath.Join(root, ".claude", "skills", "oberth-triage", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\nname: mine\n---\n\nHand written.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(InstallOptions{Root: root, Target: TargetClaude, Only: "oberth-triage"}); err == nil {
		t.Fatal("a hand-written skill file was overwritten without --force")
	}
	if body := read(t, path); !strings.Contains(body, "Hand written.") {
		t.Fatalf("the file was modified anyway:\n%s", body)
	}
	if _, err := Install(InstallOptions{Root: root, Target: TargetClaude, Only: "oberth-triage", Force: true}); err != nil {
		t.Fatalf("--force did not override: %v", err)
	}
	if body := read(t, path); strings.Contains(body, "Hand written.") {
		t.Fatal("--force did not replace the file")
	}
}

func TestInstallRefusesASymlinkedDestination(t *testing.T) {
	t.Parallel()
	root := repo(t)
	elsewhere := filepath.Join(t.TempDir(), "target.md")
	if err := os.WriteFile(elsewhere, []byte("elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(root, "AGENTS.md")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, err := Install(InstallOptions{Root: root, Target: TargetAgents, Force: true}); err == nil {
		t.Fatal("a symlinked AGENTS.md was followed")
	}
	if read(t, elsewhere) != "elsewhere\n" {
		t.Fatal("the symlink target was written through")
	}
}

func TestInstallRefusesAnUnknownSkillOrTarget(t *testing.T) {
	t.Parallel()
	root := repo(t)
	if _, err := Install(InstallOptions{Root: root, Target: TargetClaude, Only: "nope"}); err == nil {
		t.Fatal("unknown skill accepted")
	}
	if _, err := Install(InstallOptions{Root: root, Target: Target("cursor")}); err == nil {
		t.Fatal("unknown target accepted")
	}
}

func TestDetectPicksFromWhatTheRepositoryAlreadyHas(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		seed string
		want Target
	}{
		"nothing":     {"", TargetAgents},
		"agents file": {"AGENTS.md", TargetAgents},
		"claude dir":  {".claude/skills/x/SKILL.md", TargetClaude},
		"replit":      {"custom_instruction/instructions.md", TargetReplit},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := repo(t)
			if tc.seed != "" {
				path := filepath.Join(root, tc.seed)
				if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("x\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := Detect(root); got != tc.want {
				t.Fatalf("Detect = %q, want %q", got, tc.want)
			}
		})
	}
}
