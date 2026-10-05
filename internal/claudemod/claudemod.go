package claudemod

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed all:oberth-watch
var files embed.FS

const (
	Marketplace = "oberth"
	Plugin      = "oberth-watch"
)

const marketplaceJSON = `{
  "name": "` + Marketplace + `",
  "description": "Claude Code mods shipped with Oberth",
  "owner": { "name": "Oberth" },
  "plugins": [
    {
      "name": "` + Plugin + `",
      "description": "Watches the Oberth runs a session pushed and reports when each finishes",
      "source": "./` + Plugin + `"
    }
  ]
}
`

type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func Write(dir string) error {
	if err := os.RemoveAll(filepath.Join(dir, Plugin)); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"), []byte(marketplaceJSON), 0o600); err != nil {
		return err
	}
	return fs.WalkDir(files, Plugin, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(path))
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if strings.HasSuffix(path, ".test.ts") {
			return nil
		}
		body, err := files.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, body, 0o600)
	})
}

func Install(ctx context.Context, dir string, run Runner) error {
	if err := Write(dir); err != nil {
		return fmt.Errorf("write %s: %w", dir, err)
	}
	if out, err := run(ctx, "claude", "plugin", "marketplace", "add", dir, "--scope", "user"); err != nil {
		return fmt.Errorf("claude plugin marketplace add: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := run(ctx, "claude", "plugin", "install", Plugin+"@"+Marketplace, "--scope", "user"); err != nil {
		return fmt.Errorf("claude plugin install: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
