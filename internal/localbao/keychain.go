// Package localbao provisions and configures the OpenBao instance the
// clusterless Oberth server authenticates against.
//
// It is the one-time ceremony `oberth secretstore init --engine=docker`
// performs: a container with file storage, an initialise-and-unseal with the
// keys put beyond the reach of a `cat`, a jwt auth mount holding the public
// half of the server's signing key, a KV v2 mount, and one role and policy per
// tier. After it, the tier boundary is an OpenBao policy, which is the whole
// point: the local profile does not move that decision into Oberth's process.
//
// Everything here is idempotent. Re-running it on a configured store changes
// nothing and says so, because the alternative is an operator who is afraid to
// run it and therefore never repairs a half-finished setup.
package localbao

import (
	"context"
	"errors"
	"fmt"
	"os/user"
	"runtime"
	"strings"
)

// Keychain service names. The mechanism is the installer's: one generic
// password per secret, keyed by service and by the current account, replaced
// rather than duplicated on re-run.
const (
	UnsealKeychainService = "oberth-openbao-unseal"
	RootKeychainService   = "oberth-openbao-root" // #nosec G101 -- a keychain service name, not a credential.
)

// SecretStash reads and writes the two OpenBao credentials.
//
// An interface because the tests must not touch the developer's real login
// keychain, and because a Linux host will want secret-tool or pass here.
type SecretStash interface {
	Get(ctx context.Context, service string) (string, error)
	Put(ctx context.Context, service, value string) error
}

// ErrNoSecret is returned by Get when the service holds nothing.
var ErrNoSecret = errors.New("localbao: no stored secret")

// KeychainStash is the macOS login keychain.
type KeychainStash struct {
	// Run executes a command, returning its combined output. Injected so the
	// tests can assert the argv without running `security`.
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func (stash KeychainStash) account() (string, error) {
	// From the user database rather than $USER: the environment variable is
	// caller-controlled and is about to become a command argument.
	current, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("localbao: resolve the current user: %w", err)
	}
	return current.Username, nil
}

func (stash KeychainStash) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if stash.Run != nil {
		return stash.Run(ctx, name, args...)
	}
	return runCommand(ctx, name, args...)
}

func (stash KeychainStash) Get(ctx context.Context, service string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("localbao: no supported secret store on %s", runtime.GOOS)
	}
	account, err := stash.account()
	if err != nil {
		return "", err
	}
	output, err := stash.run(ctx, "security", "find-generic-password", "-s", service, "-a", account, "-w")
	if err != nil {
		return "", ErrNoSecret
	}
	value := strings.TrimRight(string(output), "\r\n")
	if value == "" {
		return "", ErrNoSecret
	}
	return value, nil
}

func (stash KeychainStash) Put(ctx context.Context, service, value string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("localbao: no supported secret store on %s", runtime.GOOS)
	}
	account, err := stash.account()
	if err != nil {
		return err
	}
	// -w with an inline value, and -U to replace rather than add a second
	// item under the same service. The installer's storeUplinkToken explains
	// why the value is inline: `security -w` with no value reads the
	// controlling terminal rather than stdin, so piping leaves the command
	// waiting at a prompt for a secret the caller already holds. The cost is a
	// bounded window in which the value is in this process's argv, on a
	// machine whose keychain the same user already holds.
	if _, err := stash.run(ctx, "security", "add-generic-password",
		"-s", service, "-a", account, "-U", "-w", value); err != nil {
		// Never the output: the value is one of the arguments.
		return fmt.Errorf("localbao: could not store %s in the keychain", service)
	}
	return nil
}

var _ SecretStash = KeychainStash{}
