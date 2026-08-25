package installer

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const testToken = "oberth_secrettokenvalue000000000"

func clientAccessDeps(t *testing.T, input string) (Deps, *bytes.Buffer, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	var out bytes.Buffer
	deps := Deps{
		Output:     &out,
		Input:      strings.NewReader(input),
		IsTerminal: func() bool { return true },
		KubeClient: fake.NewSimpleClientset(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "oberth-tls", Namespace: "oberth"},
			Data:       map[string][]byte{"tls.crt": []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n")},
		}),
	}
	return deps, &out, home
}

// The one moment the bearer token exists in this process is right after the
// uplink is registered. Everything the CLI and an MCP client need is derivable
// there and nowhere else without asking the operator to paste it back.
func TestClientAccessWritesWhatEachClientNeeds(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		answer  string
		wantEnv bool
		wantMCP bool
		wantCA  bool
	}{
		{"both is the default", "\n", true, true, true},
		{"cli only", "c\n", true, false, true},
		{"mcp only", "m\n", false, true, true},
		{"neither", "n\n", false, false, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			deps, _, home := clientAccessDeps(t, testCase.answer)
			tw := newTableWriter(deps.Output, false)

			if err := offerClientAccess(context.Background(), Config{}, deps, tw, testToken); err != nil {
				t.Fatalf("offer returned an error, which must never fail an install: %v", err)
			}

			envPath := filepath.Join(home, ".config", "oberth", "env")
			mcpPath := filepath.Join(home, ".config", "oberth", "mcp.json")
			caPath := filepath.Join(home, ".config", "oberth", "ca.crt")

			assertExists(t, envPath, testCase.wantEnv)
			assertExists(t, mcpPath, testCase.wantMCP)
			assertExists(t, caPath, testCase.wantCA)
		})
	}
}

// The contract docs/remote-cli.md states is that no token is written to disk.
// This is the test that has to be able to fail: it reads every file the offer
// produced and looks for the token itself.
func TestClientAccessNeverWritesTheTokenToDisk(t *testing.T) {
	deps, out, home := clientAccessDeps(t, "\n")
	tw := newTableWriter(deps.Output, false)

	if err := offerClientAccess(context.Background(), Config{}, deps, tw, testToken); err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(home, ".config", "oberth")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("nothing was written: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the default choice wrote no files")
	}
	for _, entry := range entries {
		body, readErr := os.ReadFile(filepath.Join(root, entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(body), testToken) {
			t.Errorf("%s contains the bearer token; it must come from the token command instead", entry.Name())
		}
	}
	if strings.Contains(out.String(), testToken) {
		t.Error("the offer echoed the token into its own output")
	}
}

// Nothing here may fail an install. A cluster that will not answer for its own
// TLS Secret is a reason to tell the operator to finish by hand, not a reason
// to abandon a deployment that is otherwise up.
func TestClientAccessSurvivesAClusterThatWillNotAnswer(t *testing.T) {
	deps, _, _ := clientAccessDeps(t, "\n")
	deps.KubeClient = fake.NewSimpleClientset() // no oberth-tls Secret
	tw := newTableWriter(deps.Output, false)

	if err := offerClientAccess(context.Background(), Config{}, deps, tw, testToken); err != nil {
		t.Fatalf("a missing TLS Secret failed the install: %v", err)
	}
}

// Without a terminal there is nobody to answer, and a prompt that reads a
// closed stdin is a hang rather than a default.
func TestClientAccessIsSkippedWithoutATerminal(t *testing.T) {
	deps, _, home := clientAccessDeps(t, "\n")
	deps.IsTerminal = func() bool { return false }
	tw := newTableWriter(deps.Output, false)

	if err := offerClientAccess(context.Background(), Config{}, deps, tw, testToken); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "oberth", "env")); err == nil {
		t.Error("a non-interactive install wrote client configuration nobody asked for")
	}
}

func assertExists(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Stat(path)
	switch {
	case want && err != nil:
		t.Errorf("%s was not written", filepath.Base(path))
	case !want && err == nil:
		t.Errorf("%s was written but not asked for", filepath.Base(path))
	}
}
