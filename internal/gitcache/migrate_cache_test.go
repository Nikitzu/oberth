package gitcache

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateToQualifiedLayout(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	// Create a flat cache directory.
	flatPath := filepath.Join(root, "oberth.git")
	if err := os.MkdirAll(flatPath, 0o700); err != nil {
		t.Fatal(err)
	}
	// Write a marker file so we can verify the directory content survives.
	if err := os.WriteFile(filepath.Join(flatPath, "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cache, err := New(Config{
		Root:     root,
		Upstream: func(repo string) (string, error) { return "/dev/null", nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	repos := map[string]RepoQualification{
		"oberth": {UpstreamName: "github", Org: "oberthci"},
	}
	if err := cache.MigrateToQualifiedLayout(repos); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Flat path should be gone.
	if _, err := os.Stat(flatPath); !os.IsNotExist(err) {
		t.Fatal("flat path should not exist after migration")
	}

	// Qualified path should exist with content.
	qualifiedPath := filepath.Join(root, "github", "oberthci", "oberth.git")
	head, err := os.ReadFile(filepath.Join(qualifiedPath, "HEAD"))
	if err != nil {
		t.Fatalf("read HEAD from qualified path: %v", err)
	}
	if string(head) != "ref: refs/heads/main\n" {
		t.Fatalf("HEAD content = %q, want ref: refs/heads/main", head)
	}
}

func TestMigrateToQualifiedLayoutIdempotent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	// Pre-create the qualified directory (already migrated).
	qualifiedPath := filepath.Join(root, "github", "oberthci", "oberth.git")
	if err := os.MkdirAll(qualifiedPath, 0o700); err != nil {
		t.Fatal(err)
	}

	cache, err := New(Config{
		Root:     root,
		Upstream: func(repo string) (string, error) { return "/dev/null", nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	repos := map[string]RepoQualification{
		"oberth": {UpstreamName: "github", Org: "oberthci"},
	}
	// Should not error even though flat path doesn't exist.
	if err := cache.MigrateToQualifiedLayout(repos); err != nil {
		t.Fatalf("idempotent migrate: %v", err)
	}
}

func TestQualifiedCachePath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	qualifier := func(name string) (RepoQualification, bool) {
		if name == "oberth" {
			return RepoQualification{UpstreamName: "github", Org: "oberthci"}, true
		}
		return RepoQualification{}, false
	}

	cache, err := New(Config{
		Root:          root,
		Upstream:      func(repo string) (string, error) { return "/dev/null", nil },
		RepoQualifier: qualifier,
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		input string
		want  string
	}{
		// Fully qualified: upstream/org/repo
		{"github/oberthci/oberth", filepath.Join(root, "github", "oberthci", "oberth.git")},
		// Bare name with qualifier available
		{"oberth", filepath.Join(root, "github", "oberthci", "oberth.git")},
		// Bare name without qualifier (falls back to flat)
		{"unknown", filepath.Join(root, "unknown.git")},
	}

	for _, tc := range tests {
		_, got, err := cache.path(tc.input)
		if err != nil {
			t.Errorf("path(%q): %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("path(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestListFlatCaches(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	// Create a flat cache and a qualified cache.
	if err := os.MkdirAll(filepath.Join(root, "oberth.git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "github", "oberthci", "terraform.git"), 0o700); err != nil {
		t.Fatal(err)
	}

	cache, err := New(Config{
		Root:     root,
		Upstream: func(repo string) (string, error) { return "/dev/null", nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	flat, err := cache.ListFlatCaches()
	if err != nil {
		t.Fatal(err)
	}
	if len(flat) != 1 || flat[0] != "oberth" {
		t.Fatalf("ListFlatCaches = %v, want [oberth]", flat)
	}
}
