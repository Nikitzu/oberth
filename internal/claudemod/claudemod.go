package claudemod

import (
	"context"
	"embed"
	"errors"
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

	versionFile = ".oberth-version"
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

func Write(dir, version string) error {
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"), []byte(marketplaceJSON), 0o600); err != nil {
		return err
	}
	staged, err := os.MkdirTemp(dir, "."+Plugin+"-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staged) }()
	err = fs.WalkDir(files, Plugin, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(staged, filepath.FromSlash(strings.TrimPrefix(path, Plugin)))
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
	if err != nil {
		return err
	}
	live := filepath.Join(dir, Plugin)
	retired := staged + ".old"
	if err := os.Rename(live, retired); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(staged, live); err != nil {
		return err
	}
	_ = os.RemoveAll(retired)
	return os.WriteFile(filepath.Join(dir, versionFile), []byte(version), 0o600)
}

func Refresh(dir, version string) error {
	if _, err := os.Stat(filepath.Join(dir, Plugin)); err != nil {
		return nil
	}
	if current, err := os.ReadFile(filepath.Join(dir, versionFile)); err == nil && string(current) == version { // #nosec G304 -- the mod's own version marker under the client configuration directory.
		return nil
	}
	return Write(dir, version)
}

func Install(ctx context.Context, dir, version string, run Runner) error {
	if err := Write(dir, version); err != nil {
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
