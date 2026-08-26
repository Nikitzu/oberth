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
