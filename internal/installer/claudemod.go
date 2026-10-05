package installer

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/oberthci/oberth/internal/claudemod"
)

func offerClaudeMod(ctx context.Context, cfg Config, deps Deps, tw *tableWriter, root string) {
	lookPath := deps.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	_, missing := lookPath("claude")

	choice := cfg.ClaudeMod
	if choice == "" && missing == nil {
		choice = promptClaudeMod(ctx, deps)
	}
	switch {
	case choice != "yes":
		tw.AppendRow("Claude Code mod", "not installed", "— skipped", false)
		return
	case missing != nil:
		tw.AppendRow("Claude Code mod", "claude is not on PATH", "⚠ manual", false)
		return
	}

	run := deps.RunCommand
	if run == nil {
		run = DefaultRunCommand
	}
	dir := filepath.Join(root, "claude-mods")
	err := claudemod.Install(ctx, dir, cfg.BinaryVersion, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return run(ctx, nil, name, args...)
	})
	if err != nil {
		tw.AppendRow("Claude Code mod", terseInstallError(err), "⚠ manual", false)
		return
	}
	tw.AppendRow("Claude Code mod", claudemod.Plugin+", "+displayPath(dir), "✓ installed", false)
}

func promptClaudeMod(ctx context.Context, deps Deps) string {
	if !isInteractive(deps) {
		return "no"
	}
	_, _ = fmt.Fprintf(deps.Output, "\nInstall the oberth-watch mod into Claude Code, for every session?\n")
	_, _ = fmt.Fprintf(deps.Output, "It shows the runs a session pushed above the prompt and tells the session when each finishes.\n")
	_, _ = fmt.Fprint(deps.Output, "Install it? [Y/n]: ")
	answer, err := readLine(ctx, deps.Input)
	if err != nil {
		return "no"
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "", "y", "yes":
		return "yes"
	default:
		return "no"
	}
}
