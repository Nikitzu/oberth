package store

import (
	"context"
	"testing"
	"time"

	"github.com/oberthci/oberth/internal/model"
)

func TestAdministrativeRegistrationsAreAtomicWithAudit(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 4, 21, 0, 0, 0, time.UTC)
	database := testStore(t, &now)
	upstream, err := database.RegisterUpstream(context.Background(), "admin@localhost", model.UpstreamSpec{
		Name: "codeberg", Kind: "ssh", BaseURL: "ssh://git@codeberg.org/acme",
	})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := database.CreateTokenCredential(context.Background(), model.TokenCredentialSpec{Name: "agent@host", Digest: make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	uplink, err := database.RegisterUplink(context.Background(), "admin@localhost", model.UplinkSpec{
		Fingerprint: "SHA256:key", Identity: "agent@host", TokenCredentialID: pending.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if upstream.ID <= 0 || uplink.TokenCredentialID != pending.ID || uplink.AuthActor != "admin@localhost" {
		t.Fatalf("upstream/uplink = %+v / %+v", upstream, uplink)
	}
	var actions int
	if err := database.db.QueryRowContext(context.Background(), `
SELECT count(*) FROM audit_actions WHERE action IN ('upstream.register', 'uplink.register')`).Scan(&actions); err != nil {
		t.Fatal(err)
	}
	if actions != 2 {
		t.Fatalf("administrative audit actions = %d", actions)
	}
}

func TestRegisterUplinkAdminRoundTrips(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	database := testStore(t, &now)

	// Register a non-admin uplink.
	pending1, err := database.CreateTokenCredential(context.Background(), model.TokenCredentialSpec{
		Name: "regular@host", Digest: make([]byte, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	regular, err := database.RegisterUplink(context.Background(), "admin@localhost", model.UplinkSpec{
		Fingerprint: "SHA256:regular", Identity: "regular@host", TokenCredentialID: pending1.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if regular.Admin {
		t.Fatal("non-admin uplink should have Admin=false")
	}

	// Register an admin uplink.
	digest2 := make([]byte, 32)
	digest2[0] = 1
	pending2, err := database.CreateTokenCredential(context.Background(), model.TokenCredentialSpec{
		Name: "admin@host", Digest: digest2,
	})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := database.RegisterUplink(context.Background(), "admin@localhost", model.UplinkSpec{
		Fingerprint: "SHA256:admin", Identity: "admin@host", TokenCredentialID: pending2.ID,
		Admin: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !admin.Admin {
		t.Fatal("admin uplink should have Admin=true")
	}

	// Verify round-trip through UplinkByFingerprint.
	readback, err := database.UplinkByFingerprint(context.Background(), "SHA256:admin")
	if err != nil {
		t.Fatal(err)
	}
	if !readback.Admin {
		t.Fatal("admin flag lost on UplinkByFingerprint round-trip")
	}

	readbackRegular, err := database.UplinkByFingerprint(context.Background(), "SHA256:regular")
	if err != nil {
		t.Fatal(err)
	}
	if readbackRegular.Admin {
		t.Fatal("non-admin flag corrupted on UplinkByFingerprint round-trip")
	}

	// Verify round-trip through ListUplinkStatuses.
	statuses, err := database.ListUplinkStatuses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	adminCount := 0
	for _, status := range statuses {
		if status.Admin {
			adminCount++
			if status.Fingerprint != "SHA256:admin" {
				t.Fatalf("unexpected admin uplink fingerprint = %s", status.Fingerprint)
			}
		}
	}
	if adminCount != 1 {
		t.Fatalf("admin uplinks in list = %d, want 1", adminCount)
	}

	// Verify audit details include admin field.
	var details string
	if err := database.db.QueryRowContext(context.Background(), `
SELECT details FROM audit_actions WHERE action = 'uplink.register' ORDER BY id DESC LIMIT 1`).Scan(&details); err != nil {
		t.Fatal(err)
	}
	if !containsString(details, `"admin":true`) {
		t.Fatalf("audit details missing admin flag: %s", details)
	}
}

func TestUplinkRotationRevokesDisplacedCredentialAtomically(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 17, 14, 0, 0, 0, time.UTC)
	database := testStore(t, &now)
	ctx := context.Background()

	digest1 := make([]byte, 32)
	digest1[0] = 0x01
	cred1, err := database.CreateTokenCredential(ctx, model.TokenCredentialSpec{Name: "agent@host", Digest: digest1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.RegisterUplink(ctx, "admin@localhost", model.UplinkSpec{
		Fingerprint: "SHA256:rotate", Identity: "agent@host", TokenCredentialID: cred1.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ActivateTokenCredential(ctx, cred1.ID); err != nil {
		t.Fatal(err)
	}

	now = now.Add(time.Second)
	digest2 := make([]byte, 32)
	digest2[0] = 0x02
	cred2, err := database.CreateTokenCredential(ctx, model.TokenCredentialSpec{Name: "agent@host-rotated", Digest: digest2})
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := database.RegisterUplink(ctx, "admin@localhost", model.UplinkSpec{
		Fingerprint: "SHA256:rotate", Identity: "agent@host", TokenCredentialID: cred2.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rotated.TokenCredentialID != cred2.ID {
		t.Fatalf("rotated uplink should point to new credential, got %s", rotated.TokenCredentialID)
	}

	var revokedAt *int64
	if err := database.db.QueryRowContext(ctx, `SELECT revoked_at FROM token_credentials WHERE id = ?`, cred1.ID).Scan(&revokedAt); err != nil {
		t.Fatal(err)
	}
	if revokedAt == nil {
		t.Fatal("displaced credential should be revoked after rotation")
	}

	var details string
	if err := database.db.QueryRowContext(ctx, `
SELECT details FROM audit_actions WHERE action = 'uplink.register' ORDER BY id DESC LIMIT 1`).Scan(&details); err != nil {
		t.Fatal(err)
	}
	if !containsString(details, cred1.ID) {
		t.Fatalf("rotation audit should record displaced credential ID, got %s", details)
	}
}

func containsString(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || len(haystack) > 0 && containsSubstring(haystack, needle))
}

func containsSubstring(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
