package gitcache

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// HTTPS upstreams authenticate with a token that belongs to a person, not to
// the server.
//
// The alternative is an SSH identity the server holds, which on an organisation
// repository has to be arranged by someone with admin rights and grants that
// server standing write access. A personal access token reaches exactly the
// repositories its owner already reaches, which is the right authority for a
// tool whose only write is "push the branch I just asked you to push".
//
// The token never appears in argv: `ps` is readable by every process of the
// same user, and a push runs long enough to be caught there. It is written to
// a file only this process can read, and git is pointed at an askpass helper
// that prints it.

const (
	askpassScriptName = "oberth-askpass"
	tokenFileName     = "upstream-token"
	// tokenUsername is what GitHub expects beside a personal access token over
	// HTTPS. Any non-empty username works; this one is conventional and makes
	// the log line say what the credential is.
	tokenUsername = "x-access-token"
)

// writeAskpass materialises the askpass helper and the token beside it, and
// returns the environment git needs to use them.
//
// Both files live in dir, which the caller owns and removes.
func writeAskpass(dir, token string) (map[string]string, error) {
	if strings.TrimSpace(token) == "" {
		return nil, nil
	}
	tokenPath := filepath.Join(dir, tokenFileName)
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		return nil, fmt.Errorf("write upstream token: %w", err)
	}
	// git calls the helper twice, once for each field, and distinguishes them
	// by the prompt on argv. Answering the password prompt with the username
	// would send the token as a username and fail with a message about the
	// username, which is a confusing way to say the password was wrong.
	script := fmt.Sprintf(`#!/bin/sh
case "$1" in
*[Uu]sername*) printf '%%s' %q ;;
*)             cat %q ;;
esac
`, tokenUsername, tokenPath)
	scriptPath := filepath.Join(dir, askpassScriptName)
	// #nosec G306 -- GIT_ASKPASS names a program git executes, so the execute
	// bit is required. 0700 is still owner-only, and the file holds no secret:
	// the token is in the 0600 file it reads.
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		return nil, fmt.Errorf("write askpass helper: %w", err)
	}
	return map[string]string{
		"GIT_ASKPASS": scriptPath,
		// Without this git prefers an interactive prompt on a terminal it does
		// not have, and hangs rather than calling the helper.
		"GIT_TERMINAL_PROMPT": "0",
	}, nil
}

// upstreamNeedsToken reports whether a remote is one the token applies to.
// An ssh:// or local upstream authenticates by other means, and handing them
// an askpass helper would be handing a credential to something that never
// asks for one.
func upstreamNeedsToken(remote string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(remote)), "https://")
}
