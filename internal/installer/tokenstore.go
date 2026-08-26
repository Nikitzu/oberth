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
		// -U replaces an existing item rather than adding a second one under
		// the same service, which is what makes a reinstall answer the read
		// with this deployment's token instead of an older one.
		//
		// The value is supplied twice because `security -w` with no inline
		// value prompts for the password and then for its confirmation.
		return runWithSecret(ctx, deps, token+"\n"+token+"\n",
			"security", "add-generic-password", "-s", "oberth-token", "-a", account.Username, "-U", "-w")
	default:
		if _, err := lookPath("secret-tool"); err == nil {
			return runWithSecret(ctx, deps, token,
				"secret-tool", "store", "--label=Oberth uplink token", "service", "oberth")
		}
		if _, err := lookPath("pass"); err == nil {
			return runWithSecret(ctx, deps, token+"\n"+token+"\n", "pass", "insert", "oberth/token")
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
	if out, err := run(ctx, []byte(secret), name, args...); err != nil {
		return fmt.Errorf("%s: %w%s", name, err, commandOutputSuffix(out))
	}
	return nil
}
