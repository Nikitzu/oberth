package auditanchor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oberthci/oberth/internal/model"
)

// FileContinuity is the rollback-external witness store for deployments that
// have no Kubernetes API to write ConfigMaps into, which is every deployment
// the docker engine serves.
//
// The property the ConfigMap implementation buys is that the witness records
// live outside the SQLite/PVC blast radius, so restoring an older database
// snapshot cannot roll the pins back with it. A file under the same data root
// as the database does NOT buy that property: a snapshot of the data
// directory takes the pins with it. What it does preserve is the rest of the
// contract, namely create-only records, canonical payloads verified on every
// read, and gap/fork detection, so a truncated or edited pin file still fails
// closed rather than passing silently.
//
// The honest summary is therefore: same failure-closed verification, weaker
// rollback separation. Point Dir at a location outside the database's own
// snapshot unit to recover the stronger property.
type FileContinuity struct {
	dir string

	mu            sync.Mutex
	pinnedLoaded  bool
	pinned        []model.AuditWitness
	intentsLoaded bool
	intents       []model.AuditWitnessIntent
}

const (
	filePinnedPrefix = "witness-"
	fileIntentPrefix = "intent-"
	fileRecordSuffix = ".json"
	// Records are written with 0o400 so an accidental in-place edit needs an
	// explicit chmod first. This is a speed bump, not a security boundary.
	fileRecordMode = 0o400
)

// NewFileContinuity opens (creating if absent) the witness directory.
func NewFileContinuity(dir string) (*FileContinuity, error) {
	trimmed := strings.TrimSpace(dir)
	if trimmed == "" {
		return nil, errors.New("audit anchor: witness directory is required for file-backed continuity")
	}
	if err := os.MkdirAll(trimmed, 0o700); err != nil {
		return nil, fmt.Errorf("audit anchor: create witness directory: %w", err)
	}
	return &FileContinuity{dir: trimmed}, nil
}

func (continuity *FileContinuity) pinnedPath(sequence int) string {
	return filepath.Join(continuity.dir, fmt.Sprintf("%s%06d%s", filePinnedPrefix, sequence, fileRecordSuffix))
}

func (continuity *FileContinuity) intentPath(sequence int) string {
	return filepath.Join(continuity.dir, fmt.Sprintf("%s%06d%s", fileIntentPrefix, sequence, fileRecordSuffix))
}

// Pinned returns the complete, ordered witness sequence. Every record is
// re-derived from its canonical payload before it is trusted; gaps and forks
// fail closed, exactly as the ConfigMap implementation does.
func (continuity *FileContinuity) Pinned(ctx context.Context) ([]model.AuditWitness, error) {
	continuity.mu.Lock()
	defer continuity.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	history, err := continuity.loadPinnedLocked()
	if err != nil {
		return nil, err
	}
	if continuity.pinnedLoaded {
		if err := verifyCachedPinnedPrefix(continuity.pinned, history); err != nil {
			return nil, err
		}
	}
	continuity.pinned = cloneAuditWitnesses(history)
	continuity.pinnedLoaded = true
	return history, nil
}

func (continuity *FileContinuity) loadPinnedLocked() ([]model.AuditWitness, error) {
	bySequence := map[int]model.AuditWitness{}
	err := continuity.eachRecord(filePinnedPrefix, func(name string, body []byte) error {
		var record continuityRecord
		if err := json.Unmarshal(body, &record); err != nil {
			return fmt.Errorf("audit anchor: decode immutable continuity record %s: %w", name, err)
		}
		if record.Sequence <= 0 {
			return fmt.Errorf("audit anchor: immutable continuity record %s has invalid sequence %d", name, record.Sequence)
		}
		digest, err := hex.DecodeString(record.AuditSHA256)
		if err != nil {
			return fmt.Errorf("audit anchor: decode immutable continuity record %s audit hash: %w", name, err)
		}
		integratedAt, err := time.Parse(time.RFC3339Nano, record.IntegratedAt)
		if err != nil {
			return fmt.Errorf("audit anchor: decode immutable continuity record %s integration time: %w", name, err)
		}
		witness := model.AuditWitness{
			UUID: record.UUID, LogIndex: record.LogIndex, IntegratedAt: integratedAt.UTC(),
			AuditID: record.AuditID, AuditSHA256: digest, PreviousUUID: record.PreviousUUID,
		}
		// The file name encodes the sequence and the payload repeats it. A
		// record whose name and content disagree is a tampered record.
		if continuity.pinnedPath(record.Sequence) != filepath.Join(continuity.dir, name) {
			return fmt.Errorf("audit anchor: immutable continuity record %s does not match its declared sequence %d", name, record.Sequence)
		}
		canonical, err := encodeContinuityRecord(record.Sequence, witness)
		if err != nil {
			return err
		}
		if string(canonical) != string(body) {
			return fmt.Errorf("audit anchor: immutable continuity record %s differs from its canonical payload", name)
		}
		if _, duplicate := bySequence[record.Sequence]; duplicate {
			return fmt.Errorf("audit anchor: duplicate immutable continuity sequence %d", record.Sequence)
		}
		bySequence[record.Sequence] = witness
		return nil
	})
	if err != nil {
		return nil, err
	}
	sequences := make([]int, 0, len(bySequence))
	for sequence := range bySequence {
		sequences = append(sequences, sequence)
	}
	sort.Ints(sequences)
	history := make([]model.AuditWitness, 0, len(sequences))
	for index, sequence := range sequences {
		if sequence != index+1 {
			return nil, fmt.Errorf("audit anchor: immutable continuity sequence has a gap before %d", sequence)
		}
		history = append(history, bySequence[sequence])
	}
	if err := validateContinuityHistory(history); err != nil {
		return nil, err
	}
	return history, nil
}

// Intents returns the immutable publication intents created before any Rekor
// side effect, so a pending suffix stays visible across a crash.
func (continuity *FileContinuity) Intents(ctx context.Context) ([]model.AuditWitnessIntent, error) {
	continuity.mu.Lock()
	defer continuity.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	intents, err := continuity.loadIntentsLocked()
	if err != nil {
		return nil, err
	}
	if continuity.intentsLoaded {
		if err := verifyCachedIntentPrefix(continuity.intents, intents); err != nil {
			return nil, err
		}
	}
	continuity.intents = cloneAuditWitnessIntents(intents)
	continuity.intentsLoaded = true
	return intents, nil
}

func (continuity *FileContinuity) loadIntentsLocked() ([]model.AuditWitnessIntent, error) {
	bySequence := map[int]model.AuditWitnessIntent{}
	err := continuity.eachRecord(fileIntentPrefix, func(name string, body []byte) error {
		var record continuityIntentRecord
		if err := json.Unmarshal(body, &record); err != nil {
			return fmt.Errorf("audit anchor: decode immutable witness intent %s: %w", name, err)
		}
		digest, err := hex.DecodeString(record.AuditSHA256)
		if err != nil {
			return fmt.Errorf("audit anchor: decode immutable witness intent %s audit hash: %w", name, err)
		}
		intent := model.AuditWitnessIntent{
			Sequence: record.Sequence, AuditID: record.AuditID,
			AuditSHA256: digest, PreviousUUID: record.PreviousUUID,
		}
		if err := validateContinuityIntent(intent); err != nil {
			return err
		}
		if continuity.intentPath(intent.Sequence) != filepath.Join(continuity.dir, name) {
			return fmt.Errorf("audit anchor: immutable witness intent %s does not match its declared sequence %d", name, intent.Sequence)
		}
		canonical, err := encodeContinuityIntent(intent)
		if err != nil {
			return err
		}
		if string(canonical) != string(body) {
			return fmt.Errorf("audit anchor: immutable witness intent %s differs from its canonical payload", name)
		}
		if _, duplicate := bySequence[intent.Sequence]; duplicate {
			return fmt.Errorf("audit anchor: duplicate immutable witness-intent sequence %d", intent.Sequence)
		}
		bySequence[intent.Sequence] = intent
		return nil
	})
	if err != nil {
		return nil, err
	}
	sequences := make([]int, 0, len(bySequence))
	for sequence := range bySequence {
		sequences = append(sequences, sequence)
	}
	sort.Ints(sequences)
	intents := make([]model.AuditWitnessIntent, 0, len(sequences))
	for index, sequence := range sequences {
		if sequence != index+1 {
			return nil, fmt.Errorf("audit anchor: immutable witness-intent sequence has a gap before %d", sequence)
		}
		intents = append(intents, bySequence[sequence])
	}
	return intents, nil
}

// Prepare records one publication intent, create-only. Re-preparing the same
// intent is idempotent; preparing a different intent at the same sequence
// fails closed.
func (continuity *FileContinuity) Prepare(ctx context.Context, intent model.AuditWitnessIntent) error {
	continuity.mu.Lock()
	defer continuity.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateContinuityIntent(intent); err != nil {
		return err
	}
	body, err := encodeContinuityIntent(intent)
	if err != nil {
		return err
	}
	if err := continuity.createOnly(continuity.intentPath(intent.Sequence), body); err != nil {
		return err
	}
	return continuity.rememberIntentLocked(intent)
}

// Reconcile proves the records already on disk are an exact prefix of the
// freshly verified history, then creates only the missing suffix.
func (continuity *FileContinuity) Reconcile(ctx context.Context, history []model.AuditWitness) error {
	continuity.mu.Lock()
	defer continuity.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateContinuityHistory(history); err != nil {
		return err
	}
	existing, err := continuity.loadPinnedLocked()
	if err != nil {
		return err
	}
	if len(existing) > len(history) {
		return fmt.Errorf("audit anchor: %d immutable continuity records exist but the verified history has only %d",
			len(existing), len(history))
	}
	if err := verifyCachedPinnedPrefix(existing, history); err != nil {
		return err
	}
	for index := len(existing); index < len(history); index++ {
		body, err := encodeContinuityRecord(index+1, history[index])
		if err != nil {
			return err
		}
		if err := continuity.createOnly(continuity.pinnedPath(index+1), body); err != nil {
			return err
		}
	}
	reloaded, err := continuity.loadPinnedLocked()
	if err != nil {
		return err
	}
	if err := verifyCachedPinnedPrefix(history, reloaded); err != nil {
		return err
	}
	if len(reloaded) != len(history) {
		return fmt.Errorf("audit anchor: read back %d immutable continuity records, expected %d", len(reloaded), len(history))
	}
	continuity.pinned = cloneAuditWitnesses(reloaded)
	continuity.pinnedLoaded = true
	return nil
}

func (continuity *FileContinuity) rememberIntentLocked(intent model.AuditWitnessIntent) error {
	if !continuity.intentsLoaded {
		return nil
	}
	if intent.Sequence == len(continuity.intents)+1 {
		continuity.intents = append(continuity.intents, intent)
		return nil
	}
	if intent.Sequence <= len(continuity.intents) {
		return nil
	}
	// A gap opened between the cached view and this intent. Drop the cache
	// rather than record a sequence the next read cannot reconcile.
	continuity.intents, continuity.intentsLoaded = nil, false
	return nil
}

// createOnly writes body exactly once. An existing file with identical content
// is success (the crash-and-retry case); different content is a fork.
func (continuity *FileContinuity) createOnly(path string, body []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileRecordMode)
	if err == nil {
		_, writeErr := file.Write(body)
		syncErr := file.Sync()
		closeErr := file.Close()
		if joined := errors.Join(writeErr, syncErr, closeErr); joined != nil {
			return fmt.Errorf("audit anchor: write immutable record %s: %w", filepath.Base(path), joined)
		}
		return nil
	}
	if !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("audit anchor: create immutable record %s: %w", filepath.Base(path), err)
	}
	existing, readErr := os.ReadFile(path)
	if readErr != nil {
		return errors.Join(fmt.Errorf("audit anchor: create immutable record %s: %w", filepath.Base(path), err),
			fmt.Errorf("read back immutable record %s: %w", filepath.Base(path), readErr))
	}
	if string(existing) != string(body) {
		return fmt.Errorf("audit anchor: immutable record %s exists with different content", filepath.Base(path))
	}
	return nil
}

func (continuity *FileContinuity) eachRecord(prefix string, visit func(name string, body []byte) error) error {
	entries, err := os.ReadDir(continuity.dir)
	if err != nil {
		return fmt.Errorf("audit anchor: read witness directory: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, fileRecordSuffix) {
			continue
		}
		body, readErr := os.ReadFile(filepath.Join(continuity.dir, name))
		if readErr != nil {
			return fmt.Errorf("audit anchor: read immutable record %s: %w", name, readErr)
		}
		if err := visit(name, body); err != nil {
			return err
		}
	}
	return nil
}

// encodeContinuityRecord is the canonical payload for one pinned witness. It
// is the same record shape the ConfigMap implementation stores under
// "record.json", so the two backends describe a witness identically.
func encodeContinuityRecord(sequence int, witness model.AuditWitness) ([]byte, error) {
	if sequence <= 0 {
		return nil, fmt.Errorf("audit anchor: invalid continuity sequence %d", sequence)
	}
	return json.Marshal(continuityRecord{
		Sequence: sequence, UUID: witness.UUID, LogIndex: witness.LogIndex,
		IntegratedAt: witness.IntegratedAt.UTC().Format(time.RFC3339Nano),
		AuditID:      witness.AuditID, AuditSHA256: hex.EncodeToString(witness.AuditSHA256),
		PreviousUUID: witness.PreviousUUID,
	})
}

func encodeContinuityIntent(intent model.AuditWitnessIntent) ([]byte, error) {
	return json.Marshal(continuityIntentRecord{
		Sequence: intent.Sequence, AuditID: intent.AuditID,
		AuditSHA256: hex.EncodeToString(intent.AuditSHA256), PreviousUUID: intent.PreviousUUID,
	})
}

var _ Continuity = (*FileContinuity)(nil)
