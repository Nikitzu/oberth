package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/oberthci/oberth/internal/model"
)

// maxPipelineDocumentBytes bounds one stored document. It is the same ceiling
// the immutable-checkout reader applies to a committed document, so a
// server-held pipeline can never be larger than one the repository could have
// carried itself.
const maxPipelineDocumentBytes = 1 << 20

// StoreRepoPipeline appends one immutable version of a server-held pipeline
// document.
//
// The version counter is assigned here, inside the same transaction that reads
// the current maximum, so two concurrent stores cannot both claim the same
// number: the unique index on (repo_id, trigger_file, version) is the backstop
// and the transaction is what keeps it from firing.
func (s *Store) StoreRepoPipeline(ctx context.Context, spec model.RepoPipelineSpec) (model.RepoPipeline, error) {
	trigger := strings.TrimSpace(spec.TriggerFile)
	storedBy := strings.TrimSpace(spec.StoredBy)
	if spec.RepoID <= 0 || trigger == "" || storedBy == "" {
		return model.RepoPipeline{}, fmt.Errorf("%w: repository, trigger file, and storing identity are required", ErrInvalid)
	}
	if !spec.Tombstone && len(spec.Document) == 0 {
		return model.RepoPipeline{}, fmt.Errorf("%w: a stored pipeline document cannot be empty", ErrInvalid)
	}
	if len(spec.Document) > maxPipelineDocumentBytes {
		return model.RepoPipeline{}, fmt.Errorf("%w: pipeline document exceeds the source-size limit", ErrInvalid)
	}
	fingerprint, err := encodeFingerprint(spec.Fingerprint)
	if err != nil {
		return model.RepoPipeline{}, err
	}
	document := spec.Document
	if document == nil {
		document = []byte{}
	}
	digest := sha256.Sum256(document)
	sum := hex.EncodeToString(digest[:])
	now := unixNano(s.now())

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.RepoPipeline{}, fmt.Errorf("begin store pipeline: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var current sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT MAX(version) FROM repo_pipelines WHERE repo_id = ? AND trigger_file = ?`,
		spec.RepoID, trigger).Scan(&current); err != nil {
		return model.RepoPipeline{}, fmt.Errorf("read current pipeline version: %w", err)
	}
	version := current.Int64 + 1

	tombstone := 0
	if spec.Tombstone {
		tombstone = 1
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO repo_pipelines(
    repo_id, trigger_file, version, document, sha256, tombstone,
    fingerprint, fingerprint_ref, stored_by, stored_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		spec.RepoID, trigger, version, document, sum, tombstone,
		fingerprint, strings.TrimSpace(spec.FingerprintRef), storedBy, now); err != nil {
		return model.RepoPipeline{}, fmt.Errorf("insert pipeline version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return model.RepoPipeline{}, fmt.Errorf("commit store pipeline: %w", err)
	}
	return model.RepoPipeline{
		RepoID: spec.RepoID, TriggerFile: trigger, Version: version,
		Document: document, SHA256: sum, Tombstone: spec.Tombstone,
		Fingerprint: spec.Fingerprint, FingerprintRef: strings.TrimSpace(spec.FingerprintRef),
		StoredBy: storedBy, StoredAt: fromUnixNano(now),
	}, nil
}

// RepoPipeline returns the current version of a repository's server-held
// document for one trigger file, tombstones included: a caller has to be able
// to tell "never stored" from "withdrawn". ErrNotFound means no version was
// ever stored.
func (s *Store) RepoPipeline(ctx context.Context, repoID int64, triggerFile string) (model.RepoPipeline, error) {
	trigger := strings.TrimSpace(triggerFile)
	if repoID <= 0 || trigger == "" {
		return model.RepoPipeline{}, fmt.Errorf("%w: repository and trigger file are required", ErrInvalid)
	}
	row := s.db.QueryRowContext(ctx, `
SELECT id, repo_id, trigger_file, version, document, sha256, tombstone,
       fingerprint, fingerprint_ref, stored_by, stored_at
FROM repo_pipelines
WHERE repo_id = ? AND trigger_file = ?
ORDER BY version DESC LIMIT 1`, repoID, trigger)
	value, err := scanRepoPipeline(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.RepoPipeline{}, fmt.Errorf("%w: no server-held pipeline for this repository", ErrNotFound)
	}
	if err != nil {
		return model.RepoPipeline{}, fmt.Errorf("read server-held pipeline: %w", err)
	}
	return value, nil
}

// RepoPipelineVersions lists every version ever stored, newest first. The
// documents are omitted: a listing is read to see what happened, and carrying
// every superseded document would make it unbounded.
func (s *Store) RepoPipelineVersions(ctx context.Context, repoID int64, triggerFile string) ([]model.RepoPipeline, error) {
	trigger := strings.TrimSpace(triggerFile)
	if repoID <= 0 || trigger == "" {
		return nil, fmt.Errorf("%w: repository and trigger file are required", ErrInvalid)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, repo_id, trigger_file, version, x'', sha256, tombstone,
       fingerprint, fingerprint_ref, stored_by, stored_at
FROM repo_pipelines
WHERE repo_id = ? AND trigger_file = ?
ORDER BY version DESC`, repoID, trigger)
	if err != nil {
		return nil, fmt.Errorf("list server-held pipeline versions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var versions []model.RepoPipeline
	for rows.Next() {
		value, scanErr := scanRepoPipeline(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan server-held pipeline version: %w", scanErr)
		}
		versions = append(versions, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list server-held pipeline versions: %w", err)
	}
	return versions, nil
}

// RecordRunPipeline records which document a run ran and whether the
// repository's generator inputs drifted from the ones the stored document was
// built against.
//
// It is written once, while the run is still active, by the engine that
// resolved the document. A terminal run is left alone: its history is what it
// ran, and a late write could only disagree with it.
func (s *Store) RecordRunPipeline(ctx context.Context, id string, record model.RunPipelineRecord) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("%w: run ID is required", ErrInvalid)
	}
	switch record.Source {
	case model.PipelineSourceCommit, model.PipelineSourceServer:
	default:
		return fmt.Errorf("%w: pipeline source must be %q or %q", ErrInvalid,
			model.PipelineSourceCommit, model.PipelineSourceServer)
	}
	drift, err := encodeDriftPaths(record.Drift)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
UPDATE runs
SET pipeline_source = ?, pipeline_sha256 = ?, pipeline_version = ?, pipeline_drift = ?, updated_at = ?
WHERE id = ? AND status IN ('queued', 'running')`,
		record.Source, record.SHA256, record.Version, drift, unixNano(s.now()), id); err != nil {
		return fmt.Errorf("record run pipeline source: %w", err)
	}
	return nil
}

func scanRepoPipeline(row rowScanner) (model.RepoPipeline, error) {
	var value model.RepoPipeline
	var tombstone, storedAt int64
	var fingerprint string
	if err := row.Scan(&value.ID, &value.RepoID, &value.TriggerFile, &value.Version,
		&value.Document, &value.SHA256, &tombstone, &fingerprint, &value.FingerprintRef,
		&value.StoredBy, &storedAt); err != nil {
		return model.RepoPipeline{}, err
	}
	value.Tombstone = tombstone != 0
	value.Fingerprint = decodeFingerprint(fingerprint)
	value.StoredAt = fromUnixNano(storedAt)
	return value, nil
}

func encodeFingerprint(fingerprint map[string]string) (string, error) {
	if len(fingerprint) == 0 {
		return "", nil
	}
	encoded, err := json.Marshal(fingerprint)
	if err != nil {
		return "", fmt.Errorf("encode pipeline input fingerprint: %w", err)
	}
	return string(encoded), nil
}

func decodeFingerprint(encoded string) map[string]string {
	if strings.TrimSpace(encoded) == "" {
		return nil
	}
	var fingerprint map[string]string
	if err := json.Unmarshal([]byte(encoded), &fingerprint); err != nil {
		return nil
	}
	return fingerprint
}

func encodeDriftPaths(paths []string) (string, error) {
	if len(paths) == 0 {
		return "", nil
	}
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	encoded, err := json.Marshal(sorted)
	if err != nil {
		return "", fmt.Errorf("encode drifted pipeline inputs: %w", err)
	}
	return string(encoded), nil
}

func decodeDriftPaths(encoded string) []string {
	if strings.TrimSpace(encoded) == "" {
		return nil
	}
	var paths []string
	if err := json.Unmarshal([]byte(encoded), &paths); err != nil {
		return nil
	}
	return paths
}

// DriftedPipelineRuns lists, per repository, the most recent run that used a
// server-held document, but only where that run recorded drift.
//
// The "most recent" filter is what keeps the warning honest: a repository that
// drifted last week and was re-stored since is not still drifting, and listing
// every historical drifted run would say it was.
func (s *Store) DriftedPipelineRuns(ctx context.Context) ([]model.PipelineDriftRun, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT repositories.name, runs.id, runs.ref, runs.pipeline_drift, runs.queued_at
FROM runs
JOIN repositories ON repositories.id = runs.repo_id
WHERE runs.queue_sequence IN (
    SELECT MAX(queue_sequence) FROM runs WHERE pipeline_source = 'server' GROUP BY repo_id
) AND runs.pipeline_drift != ''
ORDER BY repositories.name`)
	if err != nil {
		return nil, fmt.Errorf("list drifted pipeline runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var drifted []model.PipelineDriftRun
	for rows.Next() {
		var value model.PipelineDriftRun
		var encoded string
		var queuedAt int64
		if err := rows.Scan(&value.Repository, &value.RunID, &value.Ref, &encoded, &queuedAt); err != nil {
			return nil, fmt.Errorf("scan drifted pipeline run: %w", err)
		}
		value.Inputs = decodeDriftPaths(encoded)
		value.QueuedAt = fromUnixNano(queuedAt)
		drifted = append(drifted, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list drifted pipeline runs: %w", err)
	}
	return drifted, nil
}
