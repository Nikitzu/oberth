package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/oberthci/oberth/internal/model"
	"github.com/oberthci/oberth/internal/pipelinegen"
	"github.com/oberthci/oberth/internal/service"
	"github.com/oberthci/oberth/pkg/argoworkflow"
	"github.com/oberthci/oberth/pkg/periapsis"
)

// heldPipelines is an in-memory stand-in for the store, keyed the way the
// store keys: repository ID and trigger file.
type heldPipelines struct {
	pipelines map[string]model.RepoPipeline
	err       error
}

func (h *heldPipelines) RepoPipeline(_ context.Context, repoID int64, triggerFile string) (model.RepoPipeline, error) {
	if h.err != nil {
		return model.RepoPipeline{}, h.err
	}
	value, ok := h.pipelines[triggerFile]
	if !ok || value.RepoID != repoID {
		return model.RepoPipeline{}, errors.New("not found")
	}
	return value, nil
}

type recordedRuns struct {
	records map[string]model.RunPipelineRecord
}

func (r *recordedRuns) RecordRunPipeline(_ context.Context, runID string, record model.RunPipelineRecord) error {
	if r.records == nil {
		r.records = map[string]model.RunPipelineRecord{}
	}
	r.records[runID] = record
	return nil
}

func checkoutWith(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for relative, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func heldCIRequest(dir string) service.JobRequest {
	return service.JobRequest{
		Run:        model.Run{ID: "run-1", Ref: "refs/heads/main"},
		Repository: model.Repository{ID: 7, Name: "example"},
		SourceDir:  dir,
	}
}

func TestCommittedDocumentWinsOverTheServerHeldOne(t *testing.T) {
	t.Parallel()
	dir := checkoutWith(t, map[string]string{argoworkflow.BuildFile: "committed\n"})
	resolver := pipelineResolver{held: &heldPipelines{pipelines: map[string]model.RepoPipeline{
		argoworkflow.BuildFile: {RepoID: 7, Version: 4, SHA256: "held", Document: []byte("server-held\n")},
	}}}
	source, record, err := resolver.resolve(context.Background(), heldCIRequest(dir), periapsis.TriggerCI)
	if err != nil {
		t.Fatal(err)
	}
	if string(source) != "committed\n" {
		t.Fatalf("resolved %q, want the document the commit carried", source)
	}
	if record.Source != model.PipelineSourceCommit || record.Version != 0 {
		t.Fatalf("record = %+v, want a commit source with no version", record)
	}
	if record.SHA256 != documentDigest([]byte("committed\n")) {
		t.Fatalf("record sha256 = %q, want the digest of the committed bytes", record.SHA256)
	}
}

func TestServerHeldDocumentIsUsedWhenTheCommitCarriesNone(t *testing.T) {
	t.Parallel()
	dir := checkoutWith(t, map[string]string{"README.md": "hello\n"})
	resolver := pipelineResolver{held: &heldPipelines{pipelines: map[string]model.RepoPipeline{
		argoworkflow.BuildFile: {RepoID: 7, Version: 4, SHA256: "held-digest", Document: []byte("server-held\n")},
	}}}
	source, record, err := resolver.resolve(context.Background(), heldCIRequest(dir), periapsis.TriggerCI)
	if err != nil {
		t.Fatal(err)
	}
	if string(source) != "server-held\n" {
		t.Fatalf("resolved %q, want the server-held document", source)
	}
	if record.Source != model.PipelineSourceServer || record.Version != 4 || record.SHA256 != "held-digest" {
		t.Fatalf("record = %+v, want the server-held source, version and digest", record)
	}
}

func TestTombstonedPipelineFallsBackToCommitOnly(t *testing.T) {
	t.Parallel()
	dir := checkoutWith(t, map[string]string{"README.md": "hello\n"})
	resolver := pipelineResolver{held: &heldPipelines{pipelines: map[string]model.RepoPipeline{
		argoworkflow.BuildFile: {RepoID: 7, Version: 5, Tombstone: true, Document: []byte{}},
	}}}
	_, _, err := resolver.resolve(context.Background(), heldCIRequest(dir), periapsis.TriggerCI)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resolve after a tombstone = %v, want the missing-file error", err)
	}
	framed := noPipelineError(err, periapsis.TriggerCI, "example")
	if framed == nil || !strings.Contains(framed.Error(), "no pipeline configuration") {
		t.Fatalf("framed error = %v, want the familiar no-pipeline message", framed)
	}
}

func TestNoDocumentAnywhereKeepsTheOriginalError(t *testing.T) {
	t.Parallel()
	dir := checkoutWith(t, map[string]string{"README.md": "hello\n"})
	resolver := pipelineResolver{}
	_, _, err := resolver.resolve(context.Background(), heldCIRequest(dir), periapsis.TriggerCI)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resolve with no holder = %v, want the missing-file error", err)
	}
}

func TestServerHeldReleaseAndBuildAreSeparateDocuments(t *testing.T) {
	t.Parallel()
	dir := checkoutWith(t, map[string]string{"README.md": "hello\n"})
	resolver := pipelineResolver{held: &heldPipelines{pipelines: map[string]model.RepoPipeline{
		argoworkflow.ReleaseFile: {RepoID: 7, Version: 1, Document: []byte("release\n")},
	}}}
	if _, _, err := resolver.resolve(context.Background(), heldCIRequest(dir), periapsis.TriggerCI); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a held release document must not answer a CI run: %v", err)
	}
	request := heldCIRequest(dir)
	request.Run.Release = true
	source, record, err := resolver.resolve(context.Background(), request, periapsis.TriggerRelease)
	if err != nil {
		t.Fatal(err)
	}
	if string(source) != "release\n" || record.Source != model.PipelineSourceServer {
		t.Fatalf("release resolution = %q %+v", source, record)
	}
}

func TestDriftNamesTheChangedGeneratorInputs(t *testing.T) {
	t.Parallel()
	dir := checkoutWith(t, map[string]string{
		".github/workflows/build.yml": "name: build\non: push\n",
		"go.mod":                      "module example\n",
	})
	resolver := pipelineResolver{held: &heldPipelines{pipelines: map[string]model.RepoPipeline{
		argoworkflow.BuildFile: {
			RepoID: 7, Version: 2, Document: []byte("server-held\n"),
			Fingerprint: map[string]string{
				".github/workflows/build.yml": "a-different-hash",
				"go.mod":                      hashOf(t, dir, "go.mod"),
			},
		},
	}}}
	_, record, err := resolver.resolve(context.Background(), heldCIRequest(dir), periapsis.TriggerCI)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(record.Drift, []string{".github/workflows/build.yml"}) {
		t.Fatalf("drift = %v, want only the workflow whose content moved", record.Drift)
	}
}

func TestCommittedDocumentNeverReportsDrift(t *testing.T) {
	t.Parallel()
	dir := checkoutWith(t, map[string]string{
		argoworkflow.BuildFile:        "committed\n",
		".github/workflows/build.yml": "name: build\n",
	})
	resolver := pipelineResolver{held: &heldPipelines{pipelines: map[string]model.RepoPipeline{
		argoworkflow.BuildFile: {
			RepoID: 7, Version: 2, Document: []byte("server-held\n"),
			Fingerprint: map[string]string{".github/workflows/build.yml": "stale"},
		},
	}}}
	_, record, err := resolver.resolve(context.Background(), heldCIRequest(dir), periapsis.TriggerCI)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Drift) != 0 {
		t.Fatalf("a committed document reported drift %v; drift describes a server-held document only", record.Drift)
	}
}

func TestResolveAndRecordWritesTheRunRecordOnce(t *testing.T) {
	t.Parallel()
	dir := checkoutWith(t, map[string]string{"README.md": "hello\n"})
	recorder := &recordedRuns{}
	resolver := pipelineResolver{
		held: &heldPipelines{pipelines: map[string]model.RepoPipeline{
			argoworkflow.BuildFile: {RepoID: 7, Version: 9, SHA256: "digest", Document: []byte("server-held\n")},
		}},
		recorder: recorder,
	}
	if _, err := resolver.resolveAndRecord(context.Background(), heldCIRequest(dir), periapsis.TriggerCI); err != nil {
		t.Fatal(err)
	}
	record, ok := recorder.records["run-1"]
	if !ok || record.Source != model.PipelineSourceServer || record.Version != 9 {
		t.Fatalf("recorded = %+v ok=%v", record, ok)
	}
	// The read-only probes must not overwrite it.
	if _, _, err := resolver.resolve(context.Background(), heldCIRequest(dir), periapsis.TriggerCI); err != nil {
		t.Fatal(err)
	}
	if len(recorder.records) != 1 {
		t.Fatalf("resolve wrote a run record; only the submission path may")
	}
}

func hashOf(t *testing.T, dir, relative string) string {
	t.Helper()
	fingerprint := pipelinegen.FingerprintInputs(dir)
	sum, ok := fingerprint[relative]
	if !ok {
		t.Fatalf("no fingerprint entry for %q", relative)
	}
	return sum
}
