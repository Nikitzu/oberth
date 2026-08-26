package installer

import (
	"os"
	"testing"
)

// The shell already resolves this token from wherever its owner keeps it: a
// password manager, the Keychain, a file. Reading the exported variable uses
// that decision instead of reimplementing it and guessing wrong.
func TestTokenIsTakenFromTheEnvironment(t *testing.T) {
	for _, name := range upstreamTokenEnvVars {
		t.Run(name, func(t *testing.T) {
			for _, other := range upstreamTokenEnvVars {
				t.Setenv(other, "")
			}
			t.Setenv(name, "  ghp_from_the_shell  ")

			token, source := existingUpstreamToken()
			if token != "ghp_from_the_shell" {
				t.Fatalf("token = %q, want it trimmed from %s", token, name)
			}
			if source != "$"+name {
				t.Fatalf("source = %q, want %q", source, "$"+name)
			}
		})
	}
}

func TestNoEnvironmentTokenIsNotAnError(t *testing.T) {
	for _, name := range upstreamTokenEnvVars {
		t.Setenv(name, "")
	}
	token, source := existingUpstreamToken()
	if token != "" || source != "" {
		t.Fatalf("found %q from %q with nothing set", token, source)
	}
}

// Whitespace-only is nothing, or an accidental `export GITHUB_TOKEN=" "` would
// be stored and every push would fail against a forge complaining about a
// credential that looks present.
func TestBlankEnvironmentTokenCountsAsAbsent(t *testing.T) {
	for _, name := range upstreamTokenEnvVars {
		t.Setenv(name, "")
	}
	t.Setenv(upstreamTokenEnvVars[0], "   \t  ")
	if token, _ := existingUpstreamToken(); token != "" {
		t.Fatalf("whitespace was accepted as a token: %q", token)
	}
}

// Reading from a pipe cannot hide anything; it must still work, because that
// is how a scripted install and these tests drive the prompt.
func TestSecretFallsBackToAPlainReadWhenNotATerminal(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = write.WriteString("ghp_piped\n"); _ = write.Close() }()

	var out testWriter
	value, err := readSecret(t.Context(), Deps{Input: read}, &out, false, "Upstream token", "token: ")
	if err != nil {
		t.Fatal(err)
	}
	if value != "ghp_piped" {
		t.Fatalf("read %q", value)
	}
	if out.contains("ghp_piped") {
		t.Fatalf("the token was echoed:\n%s", out.body)
	}
}

type testWriter struct{ body string }

func (w *testWriter) Write(p []byte) (int, error) { w.body += string(p); return len(p), nil }
func (w *testWriter) contains(s string) bool {
	return len(w.body) >= len(s) && func() bool {
		for i := 0; i+len(s) <= len(w.body); i++ {
			if w.body[i:i+len(s)] == s {
				return true
			}
		}
		return false
	}()
}
