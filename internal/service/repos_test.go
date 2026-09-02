package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oberthci/oberth/internal/model"
	"github.com/oberthci/oberth/internal/store"
)

// fakeProbe stands in for the git cache's HEAD advertisement.
type fakeProbe struct {
	branch string
	err    error
	calls  int
}

func (probe *fakeProbe) LsRemoteDefaultBranch(context.Context, string) (string, error) {
	probe.calls++
	return probe.branch, probe.err
}

type registerFixture struct {
	api      *API
	store    *store.Store
	probe    *fakeProbe
	upstream model.Upstream
}

func newRegisterFixture(t *testing.T, probe *fakeProbe) *registerFixture {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "oberth.sqlite"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	upstream, err := database.CreateUpstream(ctx, model.UpstreamSpec{
		Name: "github", Kind: "ssh", BaseURL: "ssh://github.com/transferz",
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewAPI(APIConfig{
		Runs: database, History: database, Repositories: database, Auditor: database,
		RepositoryRegistrar: database, UpstreamCatalog: database, UpstreamProbe: probe,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &registerFixture{api: service, store: database, probe: probe, upstream: upstream}
}

// The gateway failure: registration seeded main from a flag default, and
// gateway is on master.
func TestRegisterReadsTheDefaultBranchFromTheUpstream(t *testing.T) {
	t.Parallel()
	fixture := newRegisterFixture(t, &fakeProbe{branch: "master"})

	registered, err := fixture.api.repoRegister(context.Background(), "SHA256:operator", "gateway", "github")
	if err != nil {
		t.Fatal(err)
	}
	if !registered.Created {
		t.Fatal("a first registration must report that it created something")
	}
	if registered.DefaultBranch != "master" {
		t.Fatalf("default branch = %q, want master", registered.DefaultBranch)
	}
	if registered.BranchSource == "" {
		t.Fatal("the branch source must be reported, so a fallback is visible as one")
	}
	stored, err := fixture.store.RepositoryByName(context.Background(), "gateway")
	if err != nil {
		t.Fatal(err)
	}
	if stored.DefaultBranch != "master" {
		t.Fatalf("stored default branch = %q, want master", stored.DefaultBranch)
	}
}

// onboard runs this on every invocation, so the second one must report rather
// than fail.
func TestRegisterIsIdempotent(t *testing.T) {
	t.Parallel()
	fixture := newRegisterFixture(t, &fakeProbe{branch: "master"})
	ctx := context.Background()

	if _, err := fixture.api.repoRegister(ctx, "SHA256:operator", "gateway", "github"); err != nil {
		t.Fatal(err)
	}
	again, err := fixture.api.repoRegister(ctx, "SHA256:operator", "gateway", "github")
	if err != nil {
		t.Fatalf("a second registration must not fail: %v", err)
	}
	if again.Created {
		t.Fatal("a repeat registration must not report that it created anything")
	}
	if again.DefaultBranch != "master" {
		t.Fatalf("default branch = %q, want master", again.DefaultBranch)
	}
}

// A repository registered before the probe existed carries the wrong branch
// forever unless something corrects it. The branch-mismatch error promises
// that re-running onboard does.
func TestRegisterCorrectsAWrongDefaultBranch(t *testing.T) {
	t.Parallel()
	fixture := newRegisterFixture(t, &fakeProbe{branch: "master"})
	ctx := context.Background()
	if _, err := fixture.store.CreateRepository(ctx, model.RepositorySpec{
		Name: "gateway", UpstreamID: fixture.upstream.ID, DefaultBranch: "main",
	}); err != nil {
		t.Fatal(err)
	}

	registered, err := fixture.api.repoRegister(ctx, "SHA256:operator", "gateway", "github")
	if err != nil {
		t.Fatal(err)
	}
	if !registered.BranchCorrected {
		t.Fatal("a stale default branch was not corrected")
	}
	if registered.PreviousBranch != "main" || registered.DefaultBranch != "master" {
		t.Fatalf("correction reported %q -> %q, want main -> master",
			registered.PreviousBranch, registered.DefaultBranch)
	}
	stored, err := fixture.store.RepositoryByName(ctx, "gateway")
	if err != nil {
		t.Fatal(err)
	}
	if stored.DefaultBranch != "master" {
		t.Fatalf("the correction was not persisted: %q", stored.DefaultBranch)
	}
}

// A branch that is already right must not be rewritten, or every onboard
// writes an audit entry saying nothing happened.
func TestRegisterLeavesACorrectBranchAlone(t *testing.T) {
	t.Parallel()
	fixture := newRegisterFixture(t, &fakeProbe{branch: "master"})
	ctx := context.Background()
	if _, err := fixture.api.repoRegister(ctx, "SHA256:operator", "gateway", "github"); err != nil {
		t.Fatal(err)
	}
	again, err := fixture.api.repoRegister(ctx, "SHA256:operator", "gateway", "github")
	if err != nil {
		t.Fatal(err)
	}
	if again.BranchCorrected {
		t.Fatal("a branch that was already right was reported as corrected")
	}
}

// A probe that cannot answer falls back, and says that it fell back. Silence
// here is what produced the original bug.
func TestRegisterSaysWhenTheBranchIsAFallback(t *testing.T) {
	t.Parallel()
	fixture := newRegisterFixture(t, &fakeProbe{err: errors.New("unreachable")})

	registered, err := fixture.api.repoRegister(context.Background(), "SHA256:operator", "gateway", "github")
	if err != nil {
		t.Fatal(err)
	}
	if registered.DefaultBranch != "main" {
		t.Fatalf("fallback branch = %q, want main", registered.DefaultBranch)
	}
	if !strings.Contains(registered.BranchSource, "fallback") {
		t.Fatalf("branch source = %q, want it to say it is a fallback", registered.BranchSource)
	}
}

// Remapping would move a repository under a different org's secret subtree.
func TestRegisterRefusesToRemapToAnotherUpstream(t *testing.T) {
	t.Parallel()
	fixture := newRegisterFixture(t, &fakeProbe{branch: "main"})
	ctx := context.Background()
	other, err := fixture.store.CreateUpstream(ctx, model.UpstreamSpec{
		Name: "codeberg", Kind: "ssh", BaseURL: "ssh://codeberg.org/other",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.CreateRepository(ctx, model.RepositorySpec{
		Name: "gateway", UpstreamID: other.ID, DefaultBranch: "main",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.api.repoRegister(ctx, "SHA256:operator", "gateway", "github"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("remapping = %v, want ErrInvalidInput", err)
	}
}

// One upstream is the common case and naming it carries no decision.
func TestRegisterTakesTheOnlyUpstreamWhenNoneIsNamed(t *testing.T) {
	t.Parallel()
	fixture := newRegisterFixture(t, &fakeProbe{branch: "master"})
	registered, err := fixture.api.repoRegister(context.Background(), "SHA256:operator", "gateway", "")
	if err != nil {
		t.Fatal(err)
	}
	if registered.Upstream != "github" {
		t.Fatalf("upstream = %q, want github", registered.Upstream)
	}
}

// With several, guessing would map a repository to the wrong org's secrets.
func TestRegisterRefusesToGuessAmongSeveralUpstreams(t *testing.T) {
	t.Parallel()
	fixture := newRegisterFixture(t, &fakeProbe{branch: "master"})
	if _, err := fixture.store.CreateUpstream(context.Background(), model.UpstreamSpec{
		Name: "codeberg", Kind: "ssh", BaseURL: "ssh://codeberg.org/other",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.api.repoRegister(context.Background(), "SHA256:operator", "gateway", "")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ambiguous upstream = %v, want ErrInvalidInput", err)
	}
}

func TestRegisterRefusesAnUnknownUpstream(t *testing.T) {
	t.Parallel()
	fixture := newRegisterFixture(t, &fakeProbe{branch: "master"})
	_, err := fixture.api.repoRegister(context.Background(), "SHA256:operator", "gateway", "nope")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown upstream = %v, want ErrInvalidInput", err)
	}
}
