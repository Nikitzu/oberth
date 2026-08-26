package installer

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func lookPathAllowing(names ...string) func(string) (string, error) {
	allowed := map[string]bool{}
	for _, name := range names {
		allowed[name] = true
	}
	return func(name string) (string, error) {
		if allowed[name] {
			return "/usr/local/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
}

// The whole point of detecting: a client that is not installed is never
// offered, because a yes to it writes a file nothing will ever read.
func TestUninstalledClientsAreNotOffered(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	deps := Deps{LookPath: lookPathAllowing("claude")}
	detected := detectMCPClients(deps)
	if len(detected) != 1 || detected[0].id != "claude" {
		var ids []string
		for _, c := range detected {
			ids = append(ids, c.id)
		}
		t.Fatalf("detected %v, want [claude] only", ids)
	}
}

func TestNoClientsDetectedOffersNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	deps := Deps{LookPath: lookPathAllowing()}
	if detected := detectMCPClients(deps); len(detected) != 0 {
		t.Fatalf("detected %d clients on a machine with none", len(detected))
	}
}

// Cursor installs its config directory without necessarily installing the
// shell command, so the directory has to count as detection on its own.
func TestCursorIsDetectedByItsDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0700); err != nil {
		t.Fatal(err)
	}
	deps := Deps{LookPath: lookPathAllowing()}
	detected := detectMCPClients(deps)
	if len(detected) != 1 || detected[0].id != "cursor" {
		t.Fatalf("cursor not detected by its directory: %+v", detected)
	}
}

func TestCodexBlockNamesAnEnvironmentVariableNotAToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := configureCodex("https://oberth:30443"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "[mcp_servers.oberth]") {
		t.Fatalf("no server table written:\n%s", text)
	}
	if !strings.Contains(text, `env_http_headers = { Authorization = "OBERTH_TOKEN" }`) {
		t.Fatalf("header does not reference the environment variable:\n%s", text)
	}
}

// An existing config must survive: appending is only safe because the table is
// absent, and a second run must not append it twice.
func TestCodexKeepsExistingConfigAndDoesNotDuplicate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("model = \"gpt-5\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := configureCodex("https://oberth:30443"); err != nil {
		t.Fatal(err)
	}
	detail, err := configureCodex("https://oberth:30443")
	if err != nil {
		t.Fatal(err)
	}
	if detail != "already configured" {
		t.Fatalf("second run reported %q, want it to leave the entry alone", detail)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), `model = "gpt-5"`) {
		t.Fatalf("existing configuration lost:\n%s", body)
	}
	if strings.Count(string(body), "[mcp_servers.oberth]") != 1 {
		t.Fatalf("server table written twice:\n%s", body)
	}
}

func TestCursorMergesRatherThanReplaces(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	existing := `{"mcpServers":{"other":{"url":"https://elsewhere/mcp"}}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := configureCursor("https://oberth:30443"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		MCPServers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	if _, ok := document.MCPServers["other"]; !ok {
		t.Fatalf("existing server dropped:\n%s", body)
	}
	got := document.MCPServers["oberth"].Headers["Authorization"]
	if got != "Bearer ${env:OBERTH_TOKEN}" {
		t.Fatalf("Authorization = %q, want the env interpolation and no literal token", got)
	}
	if strings.Contains(string(body), "oberth_") {
		t.Fatalf("a literal token reached the file:\n%s", body)
	}
}

// Someone's broken JSON is still their configuration; replacing it would drop
// every other server they have.
func TestCursorRefusesToOverwriteUnparseableConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := configureCursor("https://oberth:30443"); err == nil {
		t.Fatal("overwrote a config file it could not parse")
	}
	body, _ := os.ReadFile(path)
	if string(body) != "{not json" {
		t.Fatalf("file was modified: %s", body)
	}
}

// An MCP server announces its tools; a CLI announces nothing. These two are
// the whole of what makes the command discoverable, so they are worth pinning.

func TestCLIIsLinkedUnderTheNameEveryInstructionUses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	link, err := installCLIOnPath()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".local", "bin", "oberth")
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("nothing linked at %s: %v", path, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("a copy was made where a link would survive an upgrade")
	}
	if !strings.Contains(link, "oberth") {
		t.Fatalf("reported link %q does not name the command", link)
	}
	// Re-running an install must not fail on its own previous link.
	if _, err := installCLIOnPath(); err != nil {
		t.Fatalf("second install failed: %v", err)
	}
}

// Replacing a real file at that path would be overwriting another tool.
func TestCLILinkRefusesToReplaceSomeoneElsesBinary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "oberth"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := installCLIOnPath(); err == nil {
		t.Fatal("overwrote a real file at the install path")
	}
	body, _ := os.ReadFile(filepath.Join(dir, "oberth"))
	if string(body) != "#!/bin/sh\n" {
		t.Fatalf("the existing binary was modified: %q", body)
	}
}

// Codex and Cursor never read .claude/skills, so writing there for them would
// be writing to a path they do not open.
func TestSkillsGoWhereEachClientActuallyReads(t *testing.T) {
	for id, want := range map[string]string{
		"claude": "claude",
		"codex":  "agents",
		"cursor": "agents",
	} {
		if got := string(skillTargetFor(id)); got != want {
			t.Errorf("%s skills target = %q, want %q", id, got, want)
		}
	}
}

func TestSkillsAreInstalledIntoTheHomeDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	detail, err := installSkillsForClient("claude")
	if err != nil {
		t.Fatal(err)
	}
	if detail == "" {
		t.Fatal("no detail reported")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "oberth-triage", "SKILL.md")); err != nil {
		t.Fatalf("triage skill not installed where Claude Code reads: %v", err)
	}
}
