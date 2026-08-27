package auditanchor

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/oberthci/oberth/internal/model"
)

func fileWitness(sequence int, previous string) model.AuditWitness {
	return model.AuditWitness{
		UUID:         strings.Repeat(string(rune('a'+sequence)), 16),
		LogIndex:     int64(sequence),
		IntegratedAt: time.Unix(int64(1700000000+sequence), 0).UTC(),
		AuditID:      int64(sequence),
		AuditSHA256:  bytes.Repeat([]byte{byte(sequence)}, 32),
		PreviousUUID: previous,
	}
}

func newFileTestContinuity(t *testing.T) *FileContinuity {
	t.Helper()
	continuity, err := NewFileContinuity(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileContinuity: %v", err)
	}
	return continuity
}

func TestFileContinuityReconcileThenPinnedRoundTrips(t *testing.T) {
	continuity := newFileTestContinuity(t)
	first := fileWitness(1, "")
	history := []model.AuditWitness{first, fileWitness(2, first.UUID)}
	if err := continuity.Reconcile(context.Background(), history); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	pinned, err := continuity.Pinned(context.Background())
	if err != nil {
		t.Fatalf("Pinned: %v", err)
	}
	if len(pinned) != 2 || pinned[0].UUID != history[0].UUID || pinned[1].UUID != history[1].UUID {
		t.Fatalf("Pinned returned %+v, want %+v", pinned, history)
	}
	if !pinned[1].IntegratedAt.Equal(history[1].IntegratedAt) {
		t.Fatalf("IntegratedAt round trip lost precision: %v vs %v", pinned[1].IntegratedAt, history[1].IntegratedAt)
	}
}

func TestFileContinuityReconcileAppendsOnlyTheMissingSuffix(t *testing.T) {
	continuity := newFileTestContinuity(t)
	first := fileWitness(1, "")
	if err := continuity.Reconcile(context.Background(), []model.AuditWitness{first}); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	extended := []model.AuditWitness{first, fileWitness(2, first.UUID)}
	if err := continuity.Reconcile(context.Background(), extended); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	pinned, err := continuity.Pinned(context.Background())
	if err != nil {
		t.Fatalf("Pinned: %v", err)
	}
	if len(pinned) != 2 {
		t.Fatalf("expected 2 pinned records, got %d", len(pinned))
	}
}

// A rollback that replaces the verified history with a shorter or divergent
// one must fail closed rather than quietly re-pin.
func TestFileContinuityRefusesForkedHistory(t *testing.T) {
	continuity := newFileTestContinuity(t)
	first := fileWitness(1, "")
	if err := continuity.Reconcile(context.Background(), []model.AuditWitness{first, fileWitness(2, first.UUID)}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	forked := fileWitness(9, "")
	err := continuity.Reconcile(context.Background(), []model.AuditWitness{forked, fileWitness(2, forked.UUID)})
	if err == nil {
		t.Fatal("expected a forked history to be refused")
	}
}

func TestFileContinuityRefusesShorterHistory(t *testing.T) {
	continuity := newFileTestContinuity(t)
	first := fileWitness(1, "")
	if err := continuity.Reconcile(context.Background(), []model.AuditWitness{first, fileWitness(2, first.UUID)}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := continuity.Reconcile(context.Background(), []model.AuditWitness{first}); err == nil {
		t.Fatal("expected a truncated history to be refused")
	}
}

// An edited pin file must not be trusted: the canonical payload is recomputed
// on every read.
func TestFileContinuityRefusesEditedRecord(t *testing.T) {
	continuity := newFileTestContinuity(t)
	if err := continuity.Reconcile(context.Background(), []model.AuditWitness{fileWitness(1, "")}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	path := continuity.pinnedPath(1)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"sequence":1,"uuid":"tampered"}`), 0o600); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	fresh, err := NewFileContinuity(continuity.dir)
	if err != nil {
		t.Fatalf("NewFileContinuity: %v", err)
	}
	if _, err := fresh.Pinned(context.Background()); err == nil {
		t.Fatal("expected an edited continuity record to be refused")
	}
}

func TestFileContinuityPrepareIsIdempotentAndRefusesConflict(t *testing.T) {
	continuity := newFileTestContinuity(t)
	intent := model.AuditWitnessIntent{Sequence: 1, AuditID: 7, AuditSHA256: bytes.Repeat([]byte{3}, 32)}
	if err := continuity.Prepare(context.Background(), intent); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := continuity.Prepare(context.Background(), intent); err != nil {
		t.Fatalf("re-Prepare of the identical intent must succeed: %v", err)
	}
	conflicting := intent
	conflicting.AuditID = 8
	if err := continuity.Prepare(context.Background(), conflicting); err == nil {
		t.Fatal("expected a conflicting intent at the same sequence to be refused")
	}
	intents, err := continuity.Intents(context.Background())
	if err != nil {
		t.Fatalf("Intents: %v", err)
	}
	if len(intents) != 1 || intents[0].AuditID != 7 {
		t.Fatalf("Intents returned %+v", intents)
	}
}
