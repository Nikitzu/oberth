package installer

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// upstreamTokenEnvVars are read, in order, before anyone is asked to type a
// credential.
//
// The environment is the whole mechanism deliberately. People already keep
// this token wherever they prefer -- a password manager, the Keychain, a file
// -- and their shell profile is what turns that into an exported variable.
// Reaching into any particular store from here would reimplement a decision
// the shell already made, and would guess wrong for anyone who chose
// differently.
var upstreamTokenEnvVars = []string{"GITHUB_TOKEN", "GH_TOKEN"}

// existingUpstreamToken returns a token from the environment and the name it
// came from.
func existingUpstreamToken() (token, source string) {
	for _, name := range upstreamTokenEnvVars {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value, "$" + name
		}
	}
	return "", ""
}

// readSecret reads one line without echoing it.
//
// A credential typed at a visible prompt stays in the scrollback of a terminal
// that is often shared in a screenshot, and in the recording when it is not.
//
// Falls back to a plain read when stdin is not a terminal, which is how the
// tests drive this and how a piped install still works. That path cannot hide
// anything and does not pretend to.
func readSecret(ctx context.Context, deps Deps, w io.Writer, color bool, label, prompt string) (string, error) {
	startPrompt(w, color, label, prompt)

	if file, ok := deps.Input.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		body, err := term.ReadPassword(int(file.Fd()))
		_, _ = fmt.Fprintln(w)
		erasePromptLines(w, color, 2)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(body)), nil
	}

	value, err := readLine(ctx, deps.Input)
	erasePromptLines(w, color, 1)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}
