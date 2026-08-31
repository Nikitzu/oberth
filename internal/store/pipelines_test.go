package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/oberthci/oberth/internal/model"
)

func TestStoreRepoPipelineAppendsVersionsAndNeverRewritesHistory(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	s := testStore(t, &now)
	ctx := context.Background()
	repo := createRepo(t, s)

	first, err := s.StoreRepoPipeline(ctx, model.RepoPipelineSpec{
		RepoID: repo.ID, TriggerFile: ".oberth/build.yaml", Document: []byte("first\n"),
		Fingerprint: map[string]string{"go.mod": "aaa"}, FingerprintRef: "1111111111111111111111111111111111111111",
		StoredBy: "SHA256:operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != 1 {
		t.Fatalf("first version = %d, want 1", first.Version)
	}
	digest := sha256.Sum256([]byte("first\n"))
	if first.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("first sha256 = %q, want the document digest", first.SHA256)
	}

	second, err := s.StoreRepoPipeline(ctx, model.RepoPipelineSpec{
		RepoID: repo.ID, TriggerFile: ".oberth/build.yaml", Document: []byte("second\n"),
		StoredBy: "SHA256:operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Version != 2 {
		t.Fatalf("second version = %d, want 2", second.Version)
	}

	current, err := s.RepoPipeline(ctx, repo.ID, ".oberth/build.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(current.Document) != "second\n" || current.Version != 2 {
		t.Fatalf("current = version %d %q, want version 2 second", current.Version, current.Document)
	}

	versions, err := s.RepoPipelineVersions(ctx, repo.ID, ".oberth/build.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || versions[0].Version != 2 || versions[1].Version != 1 {
		t.Fatalf("versions = %+v, want 2 then 1", versions)
	}
	// Storing a second version must not have touched the first row.
	if versions[1].Fingerprint["go.mod"] != "aaa" || versions[1].FingerprintRef == "" {
		t.Fatalf("superseded version lost its fingerprint: %+v", versions[1])
	}
}

func TestRepoPipelineRowsAreAppendOnly(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	s := testStore(t, &now)
	ctx := context.Background()
	repo := createRepo(t, s)
	if _, err := s.StoreRepoPipeline(ctx, model.RepoPipelineSpec{
		RepoID: repo.ID, TriggerFile: ".oberth/build.yaml", Document: []byte("body\n"), StoredBy: "SHA256:operator",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE repo_pipelines SET document = x'00'`); err == nil {
		t.Fatal("updating a stored pipeline version must be refused")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM repo_pipelines`); err == nil {
		t.Fatal("deleting a stored pipeline version must be refused")
	}
}

func TestRepoPipelineTombstoneIsAVersionNotADeletion(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	s := testStore(t, &now)
	ctx := context.Background()
	repo := createRepo(t, s)
	if _, err := s.StoreRepoPipeline(ctx, model.RepoPipelineSpec{
		RepoID: repo.ID, TriggerFile: ".oberth/build.yaml", Document: []byte("body\n"), StoredBy: "SHA256:operator",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StoreRepoPipeline(ctx, model.RepoPipelineSpec{
		RepoID: repo.ID, TriggerFile: ".oberth/build.yaml", Tombstone: true, StoredBy: "SHA256:operator",
	}); err != nil {
		t.Fatal(err)
	}
	current, err := s.RepoPipeline(ctx, repo.ID, ".oberth/build.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !current.Tombstone || current.Version != 2 {
		t.Fatalf("current = %+v, want a version 2 tombstone", current)
	}
	versions, err := s.RepoPipelineVersions(ctx, repo.ID, ".oberth/build.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || versions[1].SHA256 == "" {
		t.Fatalf("withdrawn version must stay readable, got %+v", versions)
	}
}

func TestRepoPipelineNotFoundWhenNeverStored(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	s := testStore(t, &now)
	repo := createRepo(t, s)
	if _, err := s.RepoPipeline(context.Background(), repo.ID, ".oberth/build.yaml"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RepoPipeline on an unstored repository = %v, want ErrNotFound", err)
	}
}

func TestRecordRunPipelineSurvivesOnTheRunRecord(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	s := testStore(t, &now)
	ctx := context.Background()
	repo := createRepo(t, s)
	enqueued, err := s.EnqueueRun(ctx, testRunSpec(repo.ID, "main", "1111111111111111111111111111111111111111"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRunPipeline(ctx, enqueued.Run.ID, model.RunPipelineRecord{
		Source: model.PipelineSourceServer, SHA256: "abc", Version: 3,
		Drift: []string{"package.json", ".github/workflows/build.yml"},
	}); err != nil {
		t.Fatal(err)
	}
	run, err := s.Run(ctx, enqueued.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.PipelineSource != model.PipelineSourceServer || run.PipelineSHA256 != "abc" || run.PipelineVersion != 3 {
		t.Fatalf("run pipeline record = %q %q %d", run.PipelineSource, run.PipelineSHA256, run.PipelineVersion)
	}
	if len(run.PipelineDrift) != 2 || run.PipelineDrift[0] != ".github/workflows/build.yml" {
		t.Fatalf("run drift = %v, want the changed paths sorted", run.PipelineDrift)
	}
}

func TestRecordRunPipelineRefusesAnUnknownSource(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	s := testStore(t, &now)
	if err := s.RecordRunPipeline(context.Background(), "run", model.RunPipelineRecord{Source: "guess"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown pipeline source = %v, want ErrInvalid", err)
	}
}
