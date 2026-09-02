package gitcache

import (
	"context"
	"testing"
	"time"
)

// TestLsRemoteDefaultBranchReadsMasterWithoutACache is the observed failure.
// Registration seeded "main" from a flag default, and a repository whose real
// default branch is master was then registered against a ref it does not
// have. The probe has to answer from the upstream's own advertisement, and it
// has to do so before any mirror exists, because registration is what runs it.
func TestLsRemoteDefaultBranchReadsMasterWithoutACache(t *testing.T) {
	t.Parallel()
	repository := newTestRepositoryWithBranch(t, "master")
	cache := newTestCache(t, repository.upstream)

	branch, err := cache.LsRemoteDefaultBranch(context.Background(), "example")
	if err != nil {
		t.Fatalf("LsRemoteDefaultBranch: %v", err)
	}
	if branch != "master" {
		t.Fatalf("default branch = %q, want master", branch)
	}
}

func TestLsRemoteDefaultBranchReadsMain(t *testing.T) {
	t.Parallel()
	repository := newTestRepositoryWithBranch(t, "main")
	cache := newTestCache(t, repository.upstream)

	branch, err := cache.LsRemoteDefaultBranch(context.Background(), "example")
	if err != nil {
		t.Fatalf("LsRemoteDefaultBranch: %v", err)
	}
	if branch != "main" {
		t.Fatalf("default branch = %q, want main", branch)
	}
}

func TestLsRemoteDefaultBranchFailsForUnreachableUpstream(t *testing.T) {
	t.Parallel()
	cache, err := New(Config{
		Root:           t.TempDir(),
		CommandTimeout: 5 * time.Second,
		Upstream: func(repo string) (string, error) {
			return "/nonexistent/upstream/" + repo + ".git", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.LsRemoteDefaultBranch(context.Background(), "example"); err == nil {
		t.Fatal("expected an error for an unreachable upstream")
	}
}

// TestBareHTTPSProbeCarriesTheUpstreamToken covers the credential half. A
// probe that names the URL in argv runs in no repository, so the config-based
// detection saw nothing and the probe went out unauthenticated: a private
// repository then reported "not found" rather than "not authenticated".
func TestBareHTTPSProbeCarriesTheUpstreamToken(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name string
		args []string
		want bool
	}{
		{"bare https ls-remote", []string{"ls-remote", "--symref", "https://example.invalid/org/repo.git", "HEAD"}, true},
		{"bare https heads probe", []string{"ls-remote", "--heads", "https://example.invalid/org/repo.git"}, true},
		{"bare ssh probe", []string{"ls-remote", "--heads", "git@example.invalid:org/repo.git"}, false},
		{"local path probe", []string{"ls-remote", "--heads", "/tmp/upstream.git"}, false},
		{"no remote command", []string{"rev-parse", "HEAD"}, false},
	} {
		if got := specTargetsHTTPS(commandSpec{args: testCase.args}); got != testCase.want {
			t.Errorf("%s: specTargetsHTTPS = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}
