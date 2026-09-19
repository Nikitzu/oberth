package dockerjob

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Maven's resources plugin copies a file only when the destination is older
// than the source, and a destination that does not exist reads as the epoch.
// A source tarred with no modification time is the epoch too, so nothing is
// ever copied and a test classpath ends up with directories and no files.
func TestTarSourceTreeCarriesModificationTimes(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src", "test", "resources"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "src", "test", "resources", "init.sql")
	if err := os.WriteFile(path, []byte("select 1;"), 0o644); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := tarSourceTree(writer, root, "src"); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	reader := tar.NewReader(&buffer)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.ModTime.IsZero() || header.ModTime.Unix() <= 0 {
			t.Fatalf("%s has no modification time in the seed tar", header.Name)
		}
		if header.Name == "src/src/test/resources/init.sql" && !header.ModTime.Equal(stamp) {
			t.Fatalf("init.sql mtime %v, want the source's %v", header.ModTime, stamp)
		}
	}
}
