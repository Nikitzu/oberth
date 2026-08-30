package secretstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oberthci/oberth/internal/localbao"
)

// The tier boundary, asserted against a live store rather than argued about.
//
// This is the claim the whole local secrets design rests on: with no
// Kubernetes, tier separation is still enforced by OpenBao's policy engine and
// not by Oberth declining to ask. The CI identity here is minted by the same
// code the server mints run identities with, and it is refused the release
// path by the store.
//
// Skipped unless OBERTH_BAO_PROBE_ADDR and OBERTH_BAO_PROBE_KEY name a store
// set up by `oberth secretstore init --engine=docker` and its signing key.
// Seed it first with:
//
//	oberth secretstore put --engine=docker oberth/upstream/acme/widget/deploy token=...
//	oberth secretstore put --engine=docker oberth/data/release/signing key=...
func liveMinter(t *testing.T) (*JWTMinter, string) {
	t.Helper()
	address := os.Getenv("OBERTH_BAO_PROBE_ADDR")
	key := os.Getenv("OBERTH_BAO_PROBE_KEY")
	if address == "" || key == "" {
		t.Skip("set OBERTH_BAO_PROBE_ADDR and OBERTH_BAO_PROBE_KEY to probe a live OpenBao")
	}
	minter, err := NewJWTMinter(key)
	if err != nil {
		t.Fatalf("NewJWTMinter: %v", err)
	}
	return minter, address
}

func liveClient(t *testing.T, minter *JWTMinter, address, role string) *Client {
	t.Helper()
	token, err := minter.Mint(context.Background(), role, "acme", "widget", "probe-run")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	// The token goes to a file because that is how the client reads it, in a
	// cluster and here: the only thing Kubernetes contributed was writing it.
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	client, err := New(Config{
		Address: address, AllowInsecureHTTP: true, CACertPEM: probeAnchor(t),
		AuthMountPath: localbao.DefaultJWTMount, Role: role, ServiceAccountTokenPath: path,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

// probeAnchor is the store's own certificate. A local store's signer is this
// machine's, so nothing in the system pool verifies it.
func probeAnchor(t *testing.T) []byte {
	t.Helper()
	path := os.Getenv("OBERTH_BAO_PROBE_CA")
	if path == "" {
		return nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the store certificate: %v", err)
	}
	return body
}

func TestLiveJWTCITierReadsItsOwnUpstreamPath(t *testing.T) {
	minter, address := liveMinter(t)
	client := liveClient(t, minter, address, localbao.DefaultCIRole)
	values, err := client.FetchKV(context.Background(), []string{"oberth/data/upstream/acme/widget/deploy"})
	if err != nil {
		t.Fatalf("the CI identity could not read its own upstream path: %v", err)
	}
	if len(values["oberth/data/upstream/acme/widget/deploy"]["token"]) == 0 {
		t.Fatal("no value returned")
	}
}

// The load-bearing assertion. A 403 from the store, not a decision from Oberth.
func TestLiveJWTCITierIsRefusedTheReleasePath(t *testing.T) {
	minter, address := liveMinter(t)
	client := liveClient(t, minter, address, localbao.DefaultCIRole)
	_, err := client.FetchKV(context.Background(), []string{"oberth/data/release/signing"})
	if err == nil {
		t.Fatal("the CI identity read a release path; tier separation is not enforced by the store")
	}
	if !strings.Contains(err.Error(), "403") && !strings.Contains(strings.ToLower(err.Error()), "permission denied") {
		t.Fatalf("the refusal did not come from the store as a permission denial: %v", err)
	}
}

func TestLiveJWTReleaseTierReadsTheReleasePath(t *testing.T) {
	minter, address := liveMinter(t)
	client := liveClient(t, minter, address, localbao.DefaultReleaseRole)
	values, err := client.FetchKV(context.Background(), []string{"oberth/data/release/signing"})
	if err != nil {
		t.Fatalf("the release identity could not read a release path: %v", err)
	}
	if len(values["oberth/data/release/signing"]["key"]) == 0 {
		t.Fatal("no value returned")
	}
}

// A subject the store does not bind must not be able to log in at all, or the
// role binding is not the boundary it is claimed to be.
func TestLiveJWTUnboundSubjectCannotLogIn(t *testing.T) {
	minter, address := liveMinter(t)
	client := liveClient(t, minter, address, "oberth-ci")
	// Same token, a role it was not minted for: the CI subject presented to
	// the release role, which binds a different subject.
	token, err := minter.Mint(context.Background(), localbao.DefaultCIRole, "acme", "widget", "probe-run")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	impostor, err := New(Config{
		Address: address, AllowInsecureHTTP: true, CACertPEM: probeAnchor(t),
		AuthMountPath: localbao.DefaultJWTMount, Role: localbao.DefaultReleaseRole, ServiceAccountTokenPath: path,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := impostor.VerifyLogin(context.Background()); err == nil {
		t.Fatal("a CI subject logged in through the release role; bound_subject is not enforced")
	}
	_ = client
}
