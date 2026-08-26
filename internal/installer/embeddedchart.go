package installer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	oberth "github.com/oberthci/oberth"
)

// embeddedChartRoot is where the carried chart is written for helm to read.
//
// helm takes a path, not a filesystem, so the embedded chart has to exist on
// disk for the length of the install. It goes in a temp directory the caller
// removes, rather than anywhere durable: a stale copy that outlived its binary
// would be a chart nobody chose.
const embeddedChartDirName = "oberth-chart"

// extractEmbeddedChart writes the carried chart to a temporary directory and
// returns the path helm should be given.
//
// The returned cleanup is always safe to call, including on error.
func extractEmbeddedChart() (string, func(), error) {
	noop := func() {}
	entries, err := fs.ReadDir(oberth.ChartFS, "charts/oberth")
	if err != nil || len(entries) == 0 {
		return "", noop, errors.New("installer: no chart is embedded in this binary")
	}

	base, err := os.MkdirTemp("", embeddedChartDirName)
	if err != nil {
		return "", noop, err
	}
	cleanup := func() { _ = os.RemoveAll(base) }

	root := filepath.Join(base, "oberth")
	err = fs.WalkDir(oberth.ChartFS, "charts/oberth", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, relErr := filepath.Rel("charts/oberth", path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(root, filepath.FromSlash(relative))
		if entry.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		body, readErr := oberth.ChartFS.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if mkErr := os.MkdirAll(filepath.Dir(target), 0o750); mkErr != nil {
			return mkErr
		}
		return os.WriteFile(target, body, 0o600)
	})
	if err != nil {
		cleanup()
		return "", noop, fmt.Errorf("installer: extract embedded chart: %w", err)
	}
	return root, cleanup, nil
}
