package installer

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	document := map[string]any{}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("settings file is no longer JSON: %v\n%s", err, body)
	}
	return document
}

// The whole file belongs to the operator. Naming the CA must leave every other
// setting, and every other variable in env, exactly where it was.
func TestCATrustIsMergedIntoTheSettingsThatAreAlreadyThere(t *testing.T) {
	t.Parallel()
	existing := []byte(`{"model":"opus","env":{"FOO":"bar"},"permissions":{"allow":["Bash"]}}`)
	body, changed, err := mergeNodeExtraCACerts(existing, "/home/me/.config/oberth/ca.crt")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	document := map[string]any{}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	if document["model"] != "opus" {
		t.Errorf("model was lost: %v", document["model"])
	}
	if document["permissions"] == nil {
		t.Error("permissions were lost")
	}
	env, _ := document["env"].(map[string]any)
	if env["FOO"] != "bar" {
		t.Errorf("an existing environment variable was lost: %v", env)
	}
	if env[nodeExtraCACerts] != "/home/me/.config/oberth/ca.crt" {
		t.Errorf("the CA was not named: %v", env)
	}
}

// A settings file with no env object at all is the common case on a machine
// that has never needed one.
func TestCATrustCreatesTheEnvObjectWhenThereIsNone(t *testing.T) {
	t.Parallel()
	body, changed, err := mergeNodeExtraCACerts([]byte(`{"model":"opus"}`), "/ca.crt")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !strings.Contains(string(body), nodeExtraCACerts) {
		t.Fatalf("env object not created:\n%s", body)
	}
}

// Re-running an install must not rewrite a file that already says the right
// thing.
func TestCATrustAlreadyNamedChangesNothing(t *testing.T) {
	t.Parallel()
	existing := []byte(`{"env":{"NODE_EXTRA_CA_CERTS":"/ca.crt"}}`)
	body, changed, err := mergeNodeExtraCACerts(existing, "/ca.crt")
	if err != nil {
		t.Fatal(err)
	}
	if changed || body != nil {
		t.Fatalf("an unchanged file was rewritten: changed=%v body=%s", changed, body)
	}
}

// A different value is another deployment's CA, or something the operator set
// deliberately. Ours is not more likely to be the right one, so this stops.
func TestCATrustDoesNotClobberADifferentValue(t *testing.T) {
	t.Parallel()
	existing := []byte(`{"env":{"NODE_EXTRA_CA_CERTS":"/somewhere/else.crt"}}`)
	_, changed, err := mergeNodeExtraCACerts(existing, "/ca.crt")
	if !errors.Is(err, errCATrustAlreadySet) {
		t.Fatalf("err=%v, want errCATrustAlreadySet", err)
	}
	if changed {
		t.Error("reported a change while refusing to make one")
	}
	if !strings.Contains(err.Error(), "/somewhere/else.crt") {
		t.Errorf("the message does not say what is already there: %v", err)
	}
}

// A file that will not parse is someone's configuration with a typo in it.
// Replacing it destroys everything else they have set.
func TestCATrustRefusesToParseFileAndLeavesItAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	broken := []byte("{\"model\": \"opus\",}\n")
	if err := os.WriteFile(path, broken, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := trustCAInClaudeCode("/ca.crt"); err == nil {
		t.Fatal("a malformed settings file was accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(broken) {
		t.Fatalf("the operator's file was destroyed:\n%s", after)
	}
}

// The end-to-end write, through the same home directory Claude Code reads.
func TestCATrustWritesTheSettingsFileClaudeCodeReads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	caPath := filepath.Join(home, ".config", "oberth", "ca.crt")

	outcome := clientCATrust("claude", caPath)
	if outcome.status != "✓ trusted" {
		t.Fatalf("status %q detail %q", outcome.status, outcome.detail)
	}
	document := readSettings(t, filepath.Join(home, ".claude", "settings.json"))
	env, _ := document["env"].(map[string]any)
	if env[nodeExtraCACerts] != caPath {
		t.Fatalf("the CA was not written: %v", document)
	}
}

// Silence is the failure this replaces: a client configured against a server
// it cannot verify, and nothing on the terminal saying so.
func TestClientsWithoutACASettingSayWhatToDoByHand(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"codex", "cursor"} {
		outcome := clientCATrust(id, "/home/me/.config/oberth/ca.crt")
		if outcome.status != "⚠ manual" {
			t.Errorf("%s: status %q, want manual", id, outcome.status)
		}
		if outcome.note == "" {
			t.Errorf("%s: nothing was said about the CA", id)
		}
	}
}
