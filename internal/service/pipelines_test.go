package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oberthci/oberth/internal/model"
	"github.com/oberthci/oberth/internal/pipelinegen"
	"github.com/oberthci/oberth/internal/store"
	"github.com/oberthci/oberth/pkg/periapsis"
)

// fakePipelineGit materializes a fixed set of files, standing in for the git
// cache. Each named ref carries its own tree, so a test can push a change by
// naming a second ref.
type fakePipelineGit struct {
	head  string
	trees map[string]map[string]string
}

func (git *fakePipelineGit) RefSHA(_ context.Context, _, _ string) (string, error) {
	if git.head == "" {
		return "", errors.New("no head")
	}
	return git.head, nil
}

func (git *fakePipelineGit) Checkout(_ context.Context, _, sha, destination string) error {
	tree, ok := git.trees[sha]
	if !ok {
		return errors.New("unknown commit " + sha)
	}
	for relative, body := range tree {
		path := filepath.Join(destination, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			return err
		}
	}
	return nil
}

const (
	firstCommit  = "1111111111111111111111111111111111111111"
	secondCommit = "2222222222222222222222222222222222222222"
)

type pipelineFixture struct {
	api   *API
	store *store.Store
	repo  model.Repository
	git   *fakePipelineGit
}

func newPipelineFixture(t *testing.T) *pipelineFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	database, err := store.Open(ctx, filepath.Join(root, "oberth.sqlite"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	upstream, err := database.CreateUpstream(ctx, model.UpstreamSpec{
		Name: "codeberg", Kind: "ssh", BaseURL: "ssh://codeberg.org/acme",
	})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := database.CreateRepository(ctx, model.RepositorySpec{
		Name: "oberth", UpstreamID: upstream.ID, DefaultBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	git := &fakePipelineGit{head: firstCommit, trees: map[string]map[string]string{
		firstCommit: {
			"go.mod":                      "module example\n\ngo 1.25\n",
			".github/workflows/build.yml": "name: build\non: push\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: go test ./...\n",
		},
		secondCommit: {
			"go.mod":                      "module example\n\ngo 1.25\n",
			".github/workflows/build.yml": "name: build\non: [push, pull_request]\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: go test ./...\n",
		},
	}}
	workspaceRoot := filepath.Join(root, "work")
	if err := os.MkdirAll(workspaceRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	service, err := NewAPI(APIConfig{
		Runs: database, History: database, Repositories: database, Auditor: database,
		PromotionWorkspaceRoot: workspaceRoot,
		Pipelines:              database,
		PipelineGit:            git,
		Upstreams:              database,
		PipelineImagePrefixes:  periapsis.DefaultRunnerImagePrefixes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &pipelineFixture{api: service, store: database, repo: repository, git: git}
}

// generatedDocument is what `oberth init` would write for the fixture's first
// commit, which is exactly what `check` regenerates.
func generatedDocument(t *testing.T, fixture *pipelineFixture, commit string) string {
	t.Helper()
	root := t.TempDir()
	if err := fixture.git.Checkout(context.Background(), "oberth", commit, root); err != nil {
		t.Fatal(err)
	}
	project := pipelinegen.DetectProject(root)
	if workflow, ok := pipelinegen.FindBuildWorkflow(root); ok {
		pipelinegen.Apply(workflow, &project)
	}
	project.Repo = "oberth"
	return pipelinegen.Generate(project).YAML
}

func TestPipelineSetRefusesADocumentAdmissionWouldRefuse(t *testing.T) {
	t.Parallel()
	fixture := newPipelineFixture(t)
	_, err := fixture.api.pipelineSet(context.Background(), "SHA256:operator", "oberth", "build",
		[]byte("this is not a workflow\n"), "")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("storing an undecodable document = %v, want ErrInvalidInput", err)
	}
	if _, err := fixture.store.RepoPipeline(context.Background(), fixture.repo.ID, ".oberth/build.yaml"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("a refused document must not have been stored")
	}
}

func TestPipelineSetRefusesAWorkflowWithADisallowedImage(t *testing.T) {
	t.Parallel()
	fixture := newPipelineFixture(t)
	original := generatedDocument(t, fixture, firstCommit)
	document := strings.ReplaceAll(original, "image: \"golang:", "image: \"evil.example.com/golang:")
	if document == original {
		t.Fatalf("the generated document names no golang image to rewrite:\n%s", original)
	}
	_, err := fixture.api.pipelineSet(context.Background(), "SHA256:operator", "oberth", "build", []byte(document), "")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("storing a document with an unallowed image = %v, want the admission refusal", err)
	}
}

func TestPipelineSetStoresAndFingerprintsTheDefaultBranchHead(t *testing.T) {
	t.Parallel()
	fixture := newPipelineFixture(t)
	document := generatedDocument(t, fixture, firstCommit)
	stored, err := fixture.api.pipelineSet(context.Background(), "SHA256:operator", "oberth", "build", []byte(document), "")
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Held || stored.Version != 1 {
		t.Fatalf("stored = %+v, want a held version 1", stored)
	}
	if stored.FingerprintRef != firstCommit {
		t.Fatalf("fingerprint ref = %q, want the default-branch head", stored.FingerprintRef)
	}
	if len(stored.Inputs) == 0 {
		t.Fatal("no generator inputs were fingerprinted")
	}
}

func TestPipelineShowReportsNeverStoredAndWithdrawnDifferently(t *testing.T) {
	t.Parallel()
	fixture := newPipelineFixture(t)
	ctx := context.Background()
	before, err := fixture.api.pipelineShow(ctx, "oberth", "build")
	if err != nil {
		t.Fatal(err)
	}
	if before.Held || before.Version != 0 {
		t.Fatalf("never-stored show = %+v, want no version at all", before)
	}
	document := generatedDocument(t, fixture, firstCommit)
	if _, err := fixture.api.pipelineSet(ctx, "SHA256:operator", "oberth", "build", []byte(document), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.api.pipelineUnset(ctx, "SHA256:operator", "oberth", "build"); err != nil {
		t.Fatal(err)
	}
	after, err := fixture.api.pipelineShow(ctx, "oberth", "build")
	if err != nil {
		t.Fatal(err)
	}
	if after.Held || after.Version != 2 {
		t.Fatalf("withdrawn show = %+v, want a version 2 that is no longer held", after)
	}
	if after.Versions != 2 {
		t.Fatalf("withdrawn show counted %d versions, want both to remain", after.Versions)
	}
}

func TestPipelineUnsetRefusesWhenNothingIsHeld(t *testing.T) {
	t.Parallel()
	fixture := newPipelineFixture(t)
	if _, err := fixture.api.pipelineUnset(context.Background(), "SHA256:operator", "oberth", "build"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unsetting nothing = %v, want ErrInvalidInput", err)
	}
}

func TestPipelineCheckReportsNoDriftAtTheStoredCommit(t *testing.T) {
	t.Parallel()
	fixture := newPipelineFixture(t)
	ctx := context.Background()
	document := generatedDocument(t, fixture, firstCommit)
	if _, err := fixture.api.pipelineSet(ctx, "SHA256:operator", "oberth", "build", []byte(document), ""); err != nil {
		t.Fatal(err)
	}
	report, err := fixture.api.pipelineCheck(ctx, "SHA256:operator", "oberth", "build", firstCommit, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Drifted || report.Diff != "" || len(report.Changed) != 0 {
		t.Fatalf("check at the stored commit = %+v, want no drift", report)
	}
}

func TestPipelineCheckNamesTheChangedInputAndCanStoreTheRegeneratedDocument(t *testing.T) {
	t.Parallel()
	fixture := newPipelineFixture(t)
	ctx := context.Background()
	document := generatedDocument(t, fixture, firstCommit)
	if _, err := fixture.api.pipelineSet(ctx, "SHA256:operator", "oberth", "build", []byte(document), ""); err != nil {
		t.Fatal(err)
	}
	report, err := fixture.api.pipelineCheck(ctx, "SHA256:operator", "oberth", "build", secondCommit, false)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Drifted {
		t.Fatalf("check after the workflow changed = %+v, want drift", report)
	}
	if len(report.Changed) != 1 || report.Changed[0] != ".github/workflows/build.yml" {
		t.Fatalf("changed = %v, want the workflow that moved", report.Changed)
	}
	if report.Stored {
		t.Fatal("check without --store must not have stored anything")
	}
	restored, err := fixture.api.pipelineCheck(ctx, "SHA256:operator", "oberth", "build", secondCommit, true)
	if err != nil {
		t.Fatal(err)
	}
	if !restored.Stored || restored.Version != 2 {
		t.Fatalf("check --store = %+v, want a stored version 2", restored)
	}
	// After re-storing, the same commit reports clean.
	final, err := fixture.api.pipelineCheck(ctx, "SHA256:operator", "oberth", "build", secondCommit, false)
	if err != nil {
		t.Fatal(err)
	}
	if final.Drifted {
		t.Fatalf("check after re-storing = %+v, want no drift", final)
	}
}

func TestPipelineCheckRefusesTheReleaseTrigger(t *testing.T) {
	t.Parallel()
	fixture := newPipelineFixture(t)
	if _, err := fixture.api.pipelineCheck(context.Background(), "SHA256:operator", "oberth", "release", "", false); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("check on the release trigger = %v, want ErrInvalidInput", err)
	}
}

func TestPipelineTriggerFileMapsTheOperatorsWord(t *testing.T) {
	t.Parallel()
	for word, want := range map[string]string{
		"": ".oberth/build.yaml", "build": ".oberth/build.yaml", "ci": ".oberth/build.yaml",
		"release": ".oberth/release.yaml", "RELEASE": ".oberth/release.yaml",
	} {
		file, _, err := pipelineTriggerFile(word)
		if err != nil || file != want {
			t.Fatalf("trigger %q = %q %v, want %q", word, file, err, want)
		}
	}
	if _, _, err := pipelineTriggerFile("plan"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown trigger = %v, want ErrInvalidInput", err)
	}
}
