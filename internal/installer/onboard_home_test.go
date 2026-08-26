package installer

import (
	"os"
	"path/filepath"
	"testing"
)

// Onboarding writes into a home directory, and a test that reaches the real
// one edits the machine it runs on. This once left ~/.ssh/config unparseable,
// which broke every git push on the developer's laptop until it was noticed.
func TestHomeDirIsInjectableSoTestsCannotEditTheRealOne(t *testing.T) {
	t.Parallel()
	sandbox := t.TempDir()
	deps := Deps{HomeDir: func() (string, error) { return sandbox, nil }}

	home, err := operatorHome(deps)
	if err != nil {
		t.Fatal(err)
	}
	if home != sandbox {
		t.Fatalf("operatorHome = %q, want the injected %q", home, sandbox)
	}

	// And the SSH-config reader looks inside it rather than the real home.
	if sshHostBlockExists(deps) {
		t.Fatal("an empty sandbox reported an existing oberth host block")
	}
	if err := os.MkdirAll(filepath.Join(sandbox, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sandbox, ".ssh", "config"), []byte("Host oberth\n  User git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !sshHostBlockExists(deps) {
		t.Fatal("the block written into the sandbox was not seen")
	}
}
