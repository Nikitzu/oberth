package installer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/oberthci/oberth/internal/skills"
)

// An MCP server describes itself: a client connects and is told the tools and
// what they do. A command-line tool describes nothing to anyone. Writing an env
// file helps a shell that already knows to source it, and tells an agent
// nothing at all.
//
// So the CLI needs two things the MCP path gets for free: to exist under a name
// on PATH, and to be written down somewhere the agent already reads.

// cliInstallDir is where the binary is linked. ~/.local/bin is on PATH in a
// default macOS and Linux shell and needs no privilege, unlike /usr/local/bin.
func cliInstallDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "bin"), nil
}

// installCLIOnPath makes the running installer reachable as `oberth`.
//
// A release binary is downloaded as oberth-darwin-arm64 and sits wherever the
// browser left it. Every instruction anywhere says `oberth`, so without this
// the first command an operator or an agent tries fails with "command not
// found" for a tool that is definitely installed.
//
// A symlink rather than a copy: an upgrade replaces the target and the name
// keeps working, and nothing is duplicated on disk.
func installCLIOnPath() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return "", err
	}
	dir, err := cliInstallDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	link := filepath.Join(dir, "oberth")

	switch existing, statErr := os.Lstat(link); {
	case statErr == nil && existing.Mode()&os.ModeSymlink != 0:
		// Ours to update: repoint it, so an upgrade does not leave the name
		// resolving to the previous binary.
		target, readErr := os.Readlink(link)
		if readErr == nil && target == self {
			return displayPath(link), nil
		}
		if err := os.Remove(link); err != nil {
			return "", err
		}
	case statErr == nil:
		// A real file someone else put there. Replacing another tool's binary
		// is not this installer's business.
		return "", fmt.Errorf("%s already exists and is not a link", displayPath(link))
	case !errors.Is(statErr, os.ErrNotExist):
		return "", statErr
	}

	if err := os.Symlink(self, link); err != nil {
		return "", err
	}
	return displayPath(link), nil
}

// onPath reports whether the install directory is actually on PATH, because
// linking into a directory the shell never searches is a silent no-op.
func onPath(dir string) bool {
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if entry == dir {
			return true
		}
	}
	return false
}

// skillTargetFor maps a client to the file it actually reads. Only Claude Code
// reads .claude/skills; Codex and Cursor both read AGENTS.md, so writing the
// Claude layout for them would be writing to a path they never open.
func skillTargetFor(clientID string) skills.Target {
	if clientID == "claude" {
		return skills.TargetClaude
	}
	return skills.TargetAgents
}

// installSkillsForClient writes Oberth's own skills into the operator's home
// directory, which is what makes the CLI discoverable: the agent reads them
// without being told this deployment exists.
func installSkillsForClient(clientID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	result, err := skills.Install(skills.InstallOptions{
		Root:     home,
		Target:   skillTargetFor(clientID),
		Personal: true,
	})
	if err != nil {
		return "", err
	}
	if len(result.Written) == 0 {
		return "already present", nil
	}
	return fmt.Sprintf("%d skills (%s)", len(result.Written), result.Target), nil
}

// cliAccessNote explains the two things an operator cannot discover from a
// results table: whether the linked name will resolve, and that an agent now
// has instructions for the CLI as well as the MCP tools.
func cliAccessNote(link, dir string) string {
	var note strings.Builder
	fmt.Fprintf(&note, "\nCLI: linked as %s\n", link)
	if !onPath(dir) {
		fmt.Fprintf(&note,
			"     %s is not on your PATH. Add it, or the name will not resolve:\n\n"+
				"         export PATH=\"%s:$PATH\"\n", displayPath(dir), displayPath(dir))
	}
	return note.String()
}
