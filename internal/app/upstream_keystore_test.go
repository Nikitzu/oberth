package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func fileStore(t *testing.T) FileUpstreamKeyStore {
	t.Helper()
	dir := t.TempDir()
	return FileUpstreamKeyStore{
		PrivateKeyPath: filepath.Join(dir, "keys", "id_ed25519"),
		PublicKeyPath:  filepath.Join(dir, "keys", "id_ed25519.pub"),
		KnownHostsPath: filepath.Join(dir, "hosts", "known_hosts"),
		MutationGate:   func(context.Context, string) error { return nil },
	}
}

// A first registration finds nothing, which is a state and not an error.
func TestFileKeyStoreLoadsNothingBeforeAnythingIsWritten(t *testing.T) {
	material, err := fileStore(t).Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(material.privateKey) != 0 || len(material.knownHosts) != 0 {
		t.Fatalf("an empty store returned material: %+v", material)
	}
}

func TestFileKeyStoreRoundTripsTheIdentityAndHostKeys(t *testing.T) {
	store := fileStore(t)
	if err := store.SaveIdentity(context.Background(), []byte("private"), []byte("public")); err != nil {
		t.Fatalf("SaveIdentity: %v", err)
	}
	if err := store.SaveKnownHosts(context.Background(), []byte("host ssh-ed25519 AAAA")); err != nil {
		t.Fatalf("SaveKnownHosts: %v", err)
	}
	material, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(material.privateKey) != "private" || string(material.publicKey) != "public" {
		t.Fatalf("identity did not round trip: %+v", material)
	}
	if string(material.knownHosts) != "host ssh-ed25519 AAAA" {
		t.Fatalf("known hosts did not round trip: %q", material.knownHosts)
	}
	info, err := os.Stat(store.PrivateKeyPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("the deploy key is mode %o", info.Mode().Perm())
	}
}

// The audit gate is the fail-closed external record of the write, so it must
// still run when the destination is a file rather than a Secret.
func TestFileKeyStoreRefusesToWriteWithoutTheAuditGate(t *testing.T) {
	store := fileStore(t)
	store.MutationGate = nil
	if err := store.SaveIdentity(context.Background(), []byte("private"), []byte("public")); err == nil {
		t.Fatal("a deploy key was written with no audit gate")
	}
	if _, err := os.Stat(store.PrivateKeyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the file was created despite the refusal")
	}
}

func TestFileKeyStoreStopsWhenTheAuditGateRefuses(t *testing.T) {
	store := fileStore(t)
	store.MutationGate = func(context.Context, string) error { return errors.New("chain is not anchored") }
	err := store.SaveKnownHosts(context.Background(), []byte("host key"))
	if err == nil || !strings.Contains(err.Error(), "chain is not anchored") {
		t.Fatalf("expected the gate's refusal, got %v", err)
	}
}

// The bootstrap must accept a caller-supplied store without demanding the
// Kubernetes names that store does not use.
func TestBootstrapValidatesWithoutKubernetesWhenGivenAStore(t *testing.T) {
	store := fileStore(t)
	bootstrap := UpstreamSSHBootstrap{
		Input: strings.NewReader(""), Output: &strings.Builder{},
		ScanHostKeys: func(context.Context, string, string) ([]ssh.PublicKey, error) { return nil, nil },
		Probe:        func(context.Context, string, []byte, []byte) error { return nil },
		KeyStore:     store,
		// No namespace, no Secret names, no data keys.
		PrivateKeyPath: store.PrivateKeyPath, KnownHostsPath: store.KnownHostsPath,
	}
	if err := bootstrap.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

// And it must still demand one of the two when given neither.
func TestBootstrapRefusesWithNeitherClientNorStore(t *testing.T) {
	bootstrap := UpstreamSSHBootstrap{
		Input: strings.NewReader(""), Output: &strings.Builder{},
		ScanHostKeys: func(context.Context, string, string) ([]ssh.PublicKey, error) { return nil, nil },
		Probe:        func(context.Context, string, []byte, []byte) error { return nil },
	}
	if err := bootstrap.validate(); err == nil {
		t.Fatal("a bootstrap with no persistence at all validated")
	}
}

// An empty known_hosts is the state a clusterless install leaves behind: the
// server expects the path to exist, so the file is created empty. The
// bootstrap must treat it as "nothing pinned yet" rather than as a corrupt pin
// file it refuses to replace, or the first registration can never happen.
func TestEmptyKnownHostsReadsAsAbsentRatherThanCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := readKnownHostsFile(path, "github.com:22")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an empty known_hosts did not read as absent: %v", err)
	}
	body, keys, selectErr := selectKnownHosts(knownHostsMaterial{}, err, nil, "github.com:22")
	if selectErr != nil {
		t.Fatalf("selectKnownHosts refused an empty file: %v", selectErr)
	}
	if len(body) != 0 || len(keys) != 0 {
		t.Fatalf("an empty file produced material: %q %v", body, keys)
	}
}

// A file that exists and does not parse is still refused: that is a pin file
// somebody wrote, and replacing it silently would drop the pins.
func TestCorruptKnownHostsIsStillRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte("this is not a known_hosts line\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := readKnownHostsFile(path, "github.com:22")
	if err == nil {
		t.Fatal("a corrupt known_hosts was accepted")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a corrupt known_hosts was reported as absent: %v", err)
	}
	if _, _, selectErr := selectKnownHosts(knownHostsMaterial{}, err, nil, "github.com:22"); selectErr == nil {
		t.Fatal("selectKnownHosts agreed to replace a corrupt pin file")
	}
}
