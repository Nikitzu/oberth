package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"

	"github.com/oberthci/oberth/internal/model"
	"github.com/oberthci/oberth/internal/pipelinegen"
	"github.com/oberthci/oberth/internal/service"
	"github.com/oberthci/oberth/pkg/argoworkflow"
	"github.com/oberthci/oberth/pkg/periapsis"
)

// PipelineHolder reads a repository's server-held pipeline document. The
// store.Store satisfies it directly.
type PipelineHolder interface {
	RepoPipeline(ctx context.Context, repoID int64, triggerFile string) (model.RepoPipeline, error)
}

// PipelineRecorder writes onto the run which document it ran. The store.Store
// satisfies it directly.
type PipelineRecorder interface {
	RecordRunPipeline(ctx context.Context, runID string, record model.RunPipelineRecord) error
}

// pipelineResolver answers one question for both execution engines: which
// bytes is this run's pipeline?
//
// There is exactly one implementation of the rule, and both engines call it,
// because two implementations would eventually disagree about which document a
// repository is running and the run record would then be a guess.
//
// The rule, in order:
//
//  1. The pushed commit carries the trigger file. The commit wins, always.
//     This is the behaviour that existed before server-held pipelines and it
//     is unchanged, including for a repository that also has a server-held
//     document: what is in the revision is what ran.
//  2. The commit does not carry it and the server holds a live document for
//     this repository and trigger. Those bytes are used.
//  3. Neither. The caller gets the original os.ErrNotExist so it can frame the
//     familiar "no pipeline configuration" error.
//
// The server-held bytes are returned to the caller unchanged and then travel
// the same path a committed document travels: the same Decode, the same
// admission, the same size bound, the same secret-path authorization. Nothing
// here decides anything about them; it only decides where they came from.
type pipelineResolver struct {
	held     PipelineHolder
	recorder PipelineRecorder
}

// resolve returns the document for one run plus the record of where it came
// from. It performs no writes.
func (resolver pipelineResolver) resolve(ctx context.Context, request service.JobRequest,
	trigger periapsis.Trigger) ([]byte, model.RunPipelineRecord, error) {
	file, err := argoworkflow.TriggerFile(trigger)
	if err != nil {
		return nil, model.RunPipelineRecord{}, err
	}
	source, commitErr := readArgoSource(request.SourceDir, trigger)
	if commitErr == nil {
		return source, model.RunPipelineRecord{
			Source: model.PipelineSourceCommit, SHA256: documentDigest(source),
		}, nil
	}
	// Anything other than "the file is not there" is a real read failure of a
	// file that does exist, and falling back to the server would hide it.
	if !errors.Is(commitErr, os.ErrNotExist) || resolver.held == nil || request.Repository.ID <= 0 {
		return nil, model.RunPipelineRecord{}, commitErr
	}
	pipeline, heldErr := resolver.held.RepoPipeline(ctx, request.Repository.ID, file)
	if heldErr != nil || pipeline.Tombstone || len(pipeline.Document) == 0 {
		return nil, model.RunPipelineRecord{}, commitErr
	}
	record := model.RunPipelineRecord{
		Source: model.PipelineSourceServer, SHA256: pipeline.SHA256, Version: pipeline.Version,
	}
	// Drift is advisory and is computed from the checkout that is already on
	// disk for this run, so it costs one directory read and cannot fail the
	// run. A version stored without a fingerprint (an operator who supplied
	// the document by hand before any commit was named) simply reports none.
	if len(pipeline.Fingerprint) > 0 {
		record.Drift = pipelinegen.DriftedInputs(pipeline.Fingerprint,
			pipelinegen.FingerprintInputs(request.SourceDir))
	}
	return pipeline.Document, record, nil
}

// resolveAndRecord is resolve plus the durable note on the run. Only the
// submission path calls it: the size, plan, and credential probes read the
// same document and must not each write the record again.
func (resolver pipelineResolver) resolveAndRecord(ctx context.Context, request service.JobRequest,
	trigger periapsis.Trigger) ([]byte, error) {
	source, record, err := resolver.resolve(ctx, request, trigger)
	if err != nil {
		return nil, err
	}
	if resolver.recorder != nil && request.Run.ID != "" {
		// A failure to record is not a reason to refuse a run that is
		// otherwise admitted: the document is already decided, and the
		// alternative is a repository that cannot build because its history
		// note could not be written.
		_ = resolver.recorder.RecordRunPipeline(ctx, request.Run.ID, record)
	}
	return source, nil
}

func documentDigest(source []byte) string {
	sum := sha256.Sum256(source)
	return hex.EncodeToString(sum[:])
}
