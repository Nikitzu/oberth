package gitcache

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestTagSHAResolvesALightweightTag(t *testing.T) {
	t.Parallel()
	repository := newTestRepository(t)
	cache := newTestCache(t, repository.upstream)
	ctx := context.Background()

	want := commitFragment(t, repository, "fragment", "kind: Workflow\n")
	runGit(t, repository.work, "tag", "v3")
	runGit(t, repository.work, "push", "origin", "v3")
	if _, err := cache.Ensure(ctx, "example"); err != nil {
		t.Fatal(err)
	}

	got, err := cache.TagSHA(ctx, "example", "v3")
	if err != nil {
		t.Fatalf("TagSHA: %v", err)
	}
	if got != want {
		t.Fatalf("TagSHA(v3) = %s, want %s", got, want)
	}
}

func TestTagSHAResolvesAnAnnotatedTagToItsCommit(t *testing.T) {
	t.Parallel()
	repository := newTestRepository(t)
	cache := newTestCache(t, repository.upstream)
	ctx := context.Background()

	want := commitFragment(t, repository, "fragment", "kind: Workflow\n")
	runGit(t, repository.work, "-c", "user.name=Test", "-c", "user.email=test@example.invalid",
		"tag", "-a", "v4", "-m", "release four")
	runGit(t, repository.work, "push", "origin", "v4")
	if _, err := cache.Ensure(ctx, "example"); err != nil {
		t.Fatal(err)
	}

	got, err := cache.TagSHA(ctx, "example", "v4")
	if err != nil {
		t.Fatalf("TagSHA: %v", err)
	}
	if got != want {
		t.Fatalf("TagSHA(v4) = %s, want the commit %s, not the tag object", got, want)
	}
}

func TestTagSHARefusesAMissingTagAndABranchName(t *testing.T) {
	t.Parallel()
	repository := newTestRepository(t)
	cache := newTestCache(t, repository.upstream)
	ctx := context.Background()
	if _, err := cache.Ensure(ctx, "example"); err != nil {
		t.Fatal(err)
	}

	if _, err := cache.TagSHA(ctx, "example", "v9"); err == nil {
		t.Fatal("TagSHA resolved a tag that does not exist")
	}
	if _, err := cache.TagSHA(ctx, "example", "main"); err == nil {
		t.Fatal("TagSHA resolved a branch name; a fragment must be pinned to a tag")
	}
}

// commitFragment writes a fragment document and commits it.
//
// Upstream keeps this beside its extracted blob.go; that file and its test are
// dropped here because fragment.go supersedes them, so the helper moves in
// with the tests that use it.
func commitFragment(t *testing.T, repository *testRepository, message, contents string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(repository.work, ".oberth"), 0o700); err != nil {
		t.Fatal(err)
	}
	return repository.commitFile(t, message, ".oberth/fragment.yaml", contents)
}
