package gitcache

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The token is a person's credential and must never reach argv: /proc and ps
// are readable by every process of the same user, and a push runs long enough
// to be caught there.
func TestTokenIsReadFromAFileNotArgv(t *testing.T) {
	dir := t.TempDir()
	env, err := writeAskpass(dir, "ghp_secret_value")
	if err != nil {
		t.Fatal(err)
	}
	script, readErr := os.ReadFile(env["GIT_ASKPASS"])
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(script), "ghp_secret_value") {
		t.Fatalf("the token was written into the helper script:\n%s", script)
	}
	body, _ := os.ReadFile(filepath.Join(dir, tokenFileName))
	if string(body) != "ghp_secret_value" {
		t.Fatalf("token file holds %q", body)
	}
	info, _ := os.Stat(filepath.Join(dir, tokenFileName))
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("token file mode is %v, want 0600", perm)
	}
}

// git asks twice and tells the two apart only by the prompt on argv. Answering
// the password prompt with the username sends the token as a username, and the
// forge then complains about the username, which is a confusing way to report
// a bad password.
func TestAskpassAnswersUsernameAndPasswordDifferently(t *testing.T) {
	dir := t.TempDir()
	env, err := writeAskpass(dir, "ghp_secret_value")
	if err != nil {
		t.Fatal(err)
	}
	helper := env["GIT_ASKPASS"]

	user, err := exec.Command(helper, "Username for 'https://github.com': ").Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(user) != tokenUsername {
		t.Fatalf("username prompt answered %q, want %q", user, tokenUsername)
	}
	password, err := exec.Command(helper, "Password for 'https://x-access-token@github.com': ").Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(password) != "ghp_secret_value" {
		t.Fatalf("password prompt answered %q", password)
	}
}

// An SSH or local upstream authenticates by other means; handing it an askpass
// helper would hand a credential to something that never asks for one.
func TestOnlyHTTPSUpstreamsTakeTheToken(t *testing.T) {
	for remote, want := range map[string]bool{
		"https://github.com/acme":   true,
		"HTTPS://github.com/acme":   true,
		"ssh://git@github.com/acme": false,
		"/data/local-upstreams":     false,
		"":                          false,
	} {
		if got := upstreamNeedsToken(remote); got != want {
			t.Errorf("upstreamNeedsToken(%q) = %v, want %v", remote, got, want)
		}
	}
}

func TestNoTokenWritesNothing(t *testing.T) {
	dir := t.TempDir()
	env, err := writeAskpass(dir, "   ")
	if err != nil {
		t.Fatal(err)
	}
	if env != nil {
		t.Fatalf("an empty token produced an environment: %v", env)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("an empty token wrote %d files", len(entries))
	}
}
