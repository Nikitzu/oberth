package store

import (
	"context"
	"testing"
	"time"
)

func TestSecretAccessGrantAndCheck(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	s := testStore(t, &now)
	ctx := context.Background()

	// No grant exists yet.
	ok, err := s.SecretAccessCheck(ctx, "terraform", "plan", "terraform/credentials")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected no active grant")
	}

	// Grant access.
	grant, err := s.Grant(ctx, "terraform", "plan", "terraform/credentials", "admin@localhost")
	if err != nil {
		t.Fatal(err)
	}
	if grant.Repo != "terraform" || grant.Step != "plan" || grant.Secret != "terraform/credentials" {
		t.Fatalf("unexpected grant: %+v", grant)
	}
	if grant.ApprovedBy != "admin@localhost" {
		t.Fatalf("approved_by = %q", grant.ApprovedBy)
	}
	if grant.RevokedAt != nil {
		t.Fatal("expected nil revoked_at")
	}

	// Check now succeeds.
	ok, err = s.SecretAccessCheck(ctx, "terraform", "plan", "terraform/credentials")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected active grant")
	}

	// Granting the same triple again returns the existing row.
	duplicate, err := s.Grant(ctx, "terraform", "plan", "terraform/credentials", "other-admin")
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.ID != grant.ID {
		t.Fatalf("duplicate grant created: got id=%d, want %d", duplicate.ID, grant.ID)
	}
}

func TestSecretAccessRevoke(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	s := testStore(t, &now)
	ctx := context.Background()

	_, err := s.Grant(ctx, "terraform", "plan", "terraform/credentials", "admin@localhost")
	if err != nil {
		t.Fatal(err)
	}

	revoked, err := s.Revoke(ctx, "terraform", "plan", "terraform/credentials", "admin@localhost")
	if err != nil {
		t.Fatal(err)
	}
	if revoked.RevokedBy != "admin@localhost" {
		t.Fatalf("revoked_by = %q", revoked.RevokedBy)
	}
	if revoked.RevokedAt == nil {
		t.Fatal("expected non-nil revoked_at")
	}

	// Check should now return false.
	ok, err := s.SecretAccessCheck(ctx, "terraform", "plan", "terraform/credentials")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected no active grant after revocation")
	}

	// Revoking again should return ErrNotFound.
	_, err = s.Revoke(ctx, "terraform", "plan", "terraform/credentials", "admin@localhost")
	if err == nil {
		t.Fatal("expected error for double revoke")
	}
}

func TestSecretAccessList(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	s := testStore(t, &now)
	ctx := context.Background()

	if _, err := s.Grant(ctx, "terraform", "plan", "terraform/credentials", "admin@localhost"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Grant(ctx, "terraform", "apply", "terraform/credentials", "admin@localhost"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Grant(ctx, "oberth", "release", "cosign-secret", "admin@localhost"); err != nil {
		t.Fatal(err)
	}

	// List active for terraform.
	grants, err := s.SecretAccessList(ctx, "terraform", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 2 {
		t.Fatalf("expected 2 grants, got %d", len(grants))
	}

	// List all repos.
	all, err := s.SecretAccessList(ctx, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 grants, got %d", len(all))
	}

	// Revoke one and list with revoked.
	if _, err := s.Revoke(ctx, "terraform", "plan", "terraform/credentials", "admin@localhost"); err != nil {
		t.Fatal(err)
	}
	active, err := s.SecretAccessList(ctx, "terraform", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("expected 1 active grant, got %d", len(active))
	}
	withRevoked, err := s.SecretAccessList(ctx, "terraform", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(withRevoked) != 2 {
		t.Fatalf("expected 2 grants (including revoked), got %d", len(withRevoked))
	}
}

func TestSecretAccessGrantAfterRevoke(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	s := testStore(t, &now)
	ctx := context.Background()

	first, err := s.Grant(ctx, "terraform", "plan", "terraform/credentials", "admin@localhost")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revoke(ctx, "terraform", "plan", "terraform/credentials", "admin@localhost"); err != nil {
		t.Fatal(err)
	}

	// Re-granting creates a new row.
	second, err := s.Grant(ctx, "terraform", "plan", "terraform/credentials", "admin@localhost")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("expected a new grant after revoke-and-regrant")
	}

	ok, err := s.SecretAccessCheck(ctx, "terraform", "plan", "terraform/credentials")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected active grant after re-grant")
	}
}

func TestActiveSecretGrants(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	s := testStore(t, &now)
	ctx := context.Background()

	if _, err := s.Grant(ctx, "terraform", "plan", "terraform/credentials", "admin@localhost"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Grant(ctx, "terraform", "plan", "terraform/state", "admin@localhost"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Grant(ctx, "terraform", "apply", "terraform/credentials", "admin@localhost"); err != nil {
		t.Fatal(err)
	}

	grants, err := s.ActiveSecretGrants(ctx, "terraform")
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(grants))
	}
	if !grants["plan"]["terraform/credentials"] {
		t.Fatal("missing plan/terraform/credentials")
	}
	if !grants["plan"]["terraform/state"] {
		t.Fatal("missing plan/terraform/state")
	}
	if !grants["apply"]["terraform/credentials"] {
		t.Fatal("missing apply/terraform/credentials")
	}
}

func TestGrantAndRevokeAppendAuditActions(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	s := testStore(t, &now)
	ctx := context.Background()

	// Empty chain — head ID is 0.
	head, err := s.VerifyAuditChain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if head.ID != 0 {
		t.Fatalf("initial chain head ID = %d, want 0", head.ID)
	}

	// Grant advances chain by exactly 1.
	grant, err := s.Grant(ctx, "terraform", "plan", "terraform/credentials", "admin@localhost")
	if err != nil {
		t.Fatal(err)
	}

	head, err = s.VerifyAuditChain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if head.ID != 1 {
		t.Fatalf("chain head after grant ID = %d, want 1", head.ID)
	}

	// Idempotent grant does NOT advance chain.
	dup, err := s.Grant(ctx, "terraform", "plan", "terraform/credentials", "other-admin")
	if err != nil {
		t.Fatal(err)
	}
	if dup.ID != grant.ID {
		t.Fatalf("idempotent grant ID = %d, want %d", dup.ID, grant.ID)
	}
	head, err = s.VerifyAuditChain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if head.ID != 1 {
		t.Fatalf("chain head after idempotent grant ID = %d, want 1 (no new action)", head.ID)
	}

	// Revoke advances chain by exactly 1 more.
	_, err = s.Revoke(ctx, "terraform", "plan", "terraform/credentials", "admin@localhost")
	if err != nil {
		t.Fatal(err)
	}
	head, err = s.VerifyAuditChain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if head.ID != 2 {
		t.Fatalf("chain head after revoke ID = %d, want 2", head.ID)
	}
}

func TestConcurrentGrantNoSpuriousError(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	s := testStore(t, &now)
	ctx := context.Background()

	type grantResult struct {
		grant SecretAccessGrant
		err   error
	}
	results := make(chan grantResult, 2)
	for i := 0; i < 2; i++ {
		go func() {
			g, err := s.Grant(ctx, "terraform", "plan", "terraform/credentials", "admin@localhost")
			results <- grantResult{g, err}
		}()
	}
	var ids []int64
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			t.Fatalf("concurrent Grant error: %v", r.err)
		}
		ids = append(ids, r.grant.ID)
	}
	if len(ids) != 2 || ids[0] != ids[1] {
		t.Fatalf("expected both grants to return the same row, got IDs %v", ids)
	}

	// Exactly one active row.
	active, err := s.SecretAccessList(ctx, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("expected 1 active grant after concurrent Grant, got %d", len(active))
	}
}

func TestSecretAccessCheckValidation(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	s := testStore(t, &now)
	ctx := context.Background()

	if _, err := s.SecretAccessCheck(ctx, "", "plan", "terraform/credentials"); err == nil {
		t.Fatal("expected error for empty repo")
	}
	if _, err := s.SecretAccessCheck(ctx, "terraform", "", "terraform/credentials"); err == nil {
		t.Fatal("expected error for empty step")
	}
	if _, err := s.SecretAccessCheck(ctx, "terraform", "plan", ""); err == nil {
		t.Fatal("expected error for empty secret")
	}
}
