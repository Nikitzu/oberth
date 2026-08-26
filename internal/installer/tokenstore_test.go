package installer

import (
	"context"
	"errors"
	"os/user"
	"runtime"
	"strings"
	"testing"
)

// An install that writes every config file and then leaves the credential
// those files need as a copy-paste has not finished: that step is the one
// people skip, and skipping it produces a client that cannot authenticate,
// reported by an MCP client as an OAuth registration failure rather than as a
// missing token.
func TestTheTokenIsStoredRatherThanPrinted(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the Keychain path is macOS only")
	}
	var argv []string
	var stdin string
	deps := Deps{RunCommand: func(_ context.Context, in []byte, name string, args ...string) ([]byte, error) {
		argv = append([]string{name}, args...)
		stdin = string(in)
		return nil, nil
	}}

	if err := storeUplinkToken(context.Background(), deps, "oberth_secret"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "add-generic-password") || !strings.Contains(joined, "-U") {
		t.Fatalf("wrong command: %s", joined)
	}
	// -U, or a reinstall adds a second item under one service and the read
	// answers with whichever came first. The account is resolved from the user
	// database rather than $USER, which is caller-controlled and about to
	// become a command argument.
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(joined, "-a "+account.Username) {
		t.Fatalf("the account is not named, so the read may match another item: %s", joined)
	}
	// Inline, because `security -w` with no value reads the controlling
	// terminal rather than stdin: piping to it stops the install at a Keychain
	// prompt asking for a password the install already holds.
	if !strings.HasSuffix(joined, "-w oberth_secret") {
		t.Fatalf("the token is not supplied to the command: %s", joined)
	}
	if stdin != "" {
		t.Fatalf("stdin was %q, want nothing: security does not read it", stdin)
	}
}

func TestAFailedStoreIsReportedNotFatal(t *testing.T) {
	deps := Deps{RunCommand: func(context.Context, []byte, string, ...string) ([]byte, error) {
		return []byte("keychain locked"), errors.New("exit status 1")
	}}
	if err := storeUplinkToken(context.Background(), deps, "oberth_secret"); err == nil {
		t.Fatal("a failed store reported success")
	}
}

func TestEmptyTokenIsNotStored(t *testing.T) {
	called := false
	deps := Deps{RunCommand: func(context.Context, []byte, string, ...string) ([]byte, error) {
		called = true
		return nil, nil
	}}
	if err := storeUplinkToken(context.Background(), deps, "   "); err == nil {
		t.Fatal("an empty token was accepted")
	}
	if called {
		t.Fatal("the secret store was invoked with nothing to store")
	}
}
