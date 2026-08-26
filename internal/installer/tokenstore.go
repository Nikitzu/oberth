package installer

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"os/user"
	"runtime"
	"strings"
)

// storeUplinkToken puts the bearer token where the configuration the install
// just wrote expects to read it.
//
// The install used to print the command and leave it to the operator. That is
// the one manual step in an otherwise complete setup, so it is the one that
// gets skipped -- and skipping it produces a client that cannot authenticate,
// reported by an MCP client as a Dynamic Client Registration failure rather
// than as a missing credential. An install that writes every config file and
// then withholds the credential those files need has not finished.
//
// The token is written to the store's standard input, never as an argument:
// argv is readable by every process of the same user.
func storeUplinkToken(ctx context.Context, deps Deps, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("no token to store")
	}
	lookPath := deps.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}

	// The account name comes from the user database rather than $USER: the
	// environment variable is caller-controlled, and it is about to become an
	// argument to a command.
	account, err := user.Current()
	if err != nil {
		return fmt.Errorf("resolve the current user: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		// The value is inline rather than on standard input.
		//
		// `security -w` with no value reads from the controlling terminal, not
		// from stdin, so piping the token to it leaves the installer sitting
		// at a Keychain prompt asking the operator for a password the install
		// already holds -- which is worse than the problem being solved.
		//
		// The cost is that the token is in this command's argv while it runs,
		// readable by another process of the same user. It is a bounded window
		// on a machine where that user already holds the Keychain, and the
		// same token is on screen in the credentials box either way.
		//
		// -U replaces an existing item rather than adding a second one under
		// the same service, which is what makes a reinstall answer the read
		// with this deployment's token instead of an older one.
		return runWithSecret(ctx, deps, "",
			"security", "add-generic-password", "-s", "oberth-token", "-a", account.Username, "-U", "-w", token)
	default:
		if _, err := lookPath("secret-tool"); err == nil {
			return runWithSecret(ctx, deps, token,
				"secret-tool", "store", "--label=Oberth uplink token", "service", "oberth")
		}
		if _, err := lookPath("pass"); err == nil {
			// --echo takes the value from stdin instead of prompting twice.
			return runWithSecret(ctx, deps, token+"\n", "pass", "insert", "--echo", "oberth/token")
		}
		return errors.New("no supported secret store found (install secret-tool or pass)")
	}
}

// runWithSecret feeds a credential to a command's standard input.
func runWithSecret(ctx context.Context, deps Deps, secret, name string, args ...string) error {
	run := deps.RunCommand
	if run == nil {
		run = DefaultRunCommand
	}
	// The error names the command but never its arguments or output: on the
	// macOS path the token is one of those arguments, and a store that fails
	// must not be the thing that prints it.
	if _, err := run(ctx, []byte(secret), name, args...); err != nil {
		return fmt.Errorf("%s could not store the token", name)
	}
	return nil
}
