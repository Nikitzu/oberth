package gitcache

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runGitStdin runs one git command with the given stdin, failing the test on
// error. It exists beside runGit (gitcache_test.go) for the batch plumbing
// commands (mktree, update-ref --stdin) that read their input stream.
func runGitStdin(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Stdin = strings.NewReader(stdin)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %q failed: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

// TestListRefsSurvivesOutputBeyondCaptureTailLimit locks in the fix for the
// 64 KB capture truncation: listRefs previously went through the tail-ring
// capture path, so a ref listing larger than maxCapturedOutput silently lost
// its head — the first surviving line was usually a partial line that failed
// SHA validation, and on an exact line boundary refs vanished with no error
// at all. listRefs feeds upstream reconciliation and replacement-ref
// cleanup, so a truncated listing is an integrity bug, not cosmetics. The
// listing must come back complete through the direct buffer.
func TestListRefsSurvivesOutputBeyondCaptureTailLimit(t *testing.T) {
	t.Parallel()

	bare := filepath.Join(t.TempDir(), "many.git")
	runGit(t, "", "init", "--quiet", "--bare", bare)
	tree := runGitStdin(t, bare, "", "mktree")
	commit := runGit(t, bare,
		"-c", "user.name=oberth-test", "-c", "user.email=test@oberth.ci",
		"commit-tree", tree, "-m", "seed")

	// ~69 bytes per ref line; 1500 refs is ~101 KB, comfortably past the
	// 64 KB tail-capture limit the old path truncated at, and nowhere near
	// the 16 MB maxRefsOutput refusal ceiling.
	const refCount = 1500
	var batch strings.Builder
	for i := 0; i < refCount; i++ {
		fmt.Fprintf(&batch, "create refs/heads/load/branch-%04d %s\n", i, commit)
	}
	runGitStdin(t, bare, batch.String(), "update-ref", "--stdin")

	cache := newTestCache(t, "unused-upstream")
	refs, err := cache.listRefs(context.Background(), bare, "refs/heads/")
	if err != nil {
		t.Fatalf("listRefs on a %d-ref repository: %v", refCount, err)
	}
	if len(refs) != refCount {
		t.Fatalf("listRefs returned %d refs, want %d — large listings must not truncate", len(refs), refCount)
	}
	for _, name := range []string{
		"refs/heads/load/branch-0000",
		fmt.Sprintf("refs/heads/load/branch-%04d", refCount-1),
	} {
		if refs[name] != commit {
			t.Fatalf("ref %s = %q, want %q", name, refs[name], commit)
		}
	}
}
