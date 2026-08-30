package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/kubernetes"
)

// kubernetesInterface is the client the in-cluster store drives, aliased so
// this file names it once.
type kubernetesInterface = kubernetes.Interface

// UpstreamKeyStore is where an upstream's deploy key and known_hosts are kept
// between the moment they are minted and the moment the server reads them.
//
// It exists because that is the only part of the SSH bootstrap that was ever
// Kubernetes shaped. Everything else about registering a real forge upstream,
// generating an identity, scanning and confirming host keys, probing
// authentication, printing the key, waiting for the operator to register it,
// is plain Go against an SSH endpoint. Putting the persistence behind an
// interface means a clusterless server runs the identical flow rather than a
// second implementation of a security-critical one.
type UpstreamKeyStore interface {
	// Load returns whatever is already persisted. Absent material is not an
	// error: a first registration has nothing to find.
	Load(ctx context.Context) (secretMaterial, error)
	// SaveIdentity persists a freshly generated deploy key and reads it back.
	SaveIdentity(ctx context.Context, privateKey, publicKey []byte) error
	// SaveKnownHosts persists the confirmed host keys and reads them back.
	SaveKnownHosts(ctx context.Context, knownHosts []byte) error
	// Describe names the store in operator-facing output.
	Describe() string
	// ActivationNote is what the operator must do for the server to pick the
	// material up, or empty when nothing is needed.
	ActivationNote() string
}

// FileUpstreamKeyStore writes the material to the same files the server reads
// at startup, which is what the projected Secret volume does in a cluster.
//
// The mutation gate still runs before each write. It is the fail-closed
// external audit gate, and it is about the write being recorded, not about
// what is being written to, so dropping it off-cluster would drop the record
// rather than an implementation detail.
type FileUpstreamKeyStore struct {
	PrivateKeyPath string
	PublicKeyPath  string
	KnownHostsPath string
	MutationGate   func(context.Context, string) error
}

func (store FileUpstreamKeyStore) Describe() string {
	return "files at " + store.PrivateKeyPath + " and " + store.KnownHostsPath
}

func (store FileUpstreamKeyStore) ActivationNote() string {
	return "Restart the server for it to read the new upstream identity; it assembles its SSH configuration once at startup."
}

func (store FileUpstreamKeyStore) Load(_ context.Context) (secretMaterial, error) {
	var material secretMaterial
	for _, entry := range []struct {
		path string
		into *[]byte
	}{
		{store.PrivateKeyPath, &material.privateKey},
		{store.PublicKeyPath, &material.publicKey},
		{store.KnownHostsPath, &material.knownHosts},
	} {
		body, err := os.ReadFile(entry.path) // #nosec G304 -- operator-supplied serve paths.
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return secretMaterial{}, fmt.Errorf("app: read upstream material %s: %w", entry.path, err)
		}
		if len(body) > maximumBootstrapMaterialBytes {
			return secretMaterial{}, fmt.Errorf("app: upstream material %s is larger than 1 MiB", entry.path)
		}
		*entry.into = body
	}
	return material, nil
}

func (store FileUpstreamKeyStore) SaveIdentity(ctx context.Context, privateKey, publicKey []byte) error {
	if err := store.write(ctx, "upstream.key.private", store.PrivateKeyPath, privateKey, 0o600); err != nil {
		return err
	}
	// The public half is not a secret, but it is written with the same mode as
	// the private one so a directory listing does not suggest otherwise.
	return store.write(ctx, "upstream.key.public", store.PublicKeyPath, publicKey, 0o600)
}

func (store FileUpstreamKeyStore) SaveKnownHosts(ctx context.Context, knownHosts []byte) error {
	return store.write(ctx, "upstream.known-hosts", store.KnownHostsPath, knownHosts, 0o600)
}

// write persists one file and reads it back, because a write that reported
// success and did not land is the failure mode the Kubernetes store's own
// read-back verification exists to catch.
func (store FileUpstreamKeyStore) write(ctx context.Context, operation, path string, body []byte, mode os.FileMode) error {
	if store.MutationGate == nil {
		return errors.New("app: audit mutation gate is required before writing upstream material")
	}
	if err := store.MutationGate(ctx, operation); err != nil {
		return fmt.Errorf("app: audit mutation gate before writing %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("app: create the directory for %s: %w", path, err)
	}
	if err := os.WriteFile(path, body, mode); err != nil {
		return fmt.Errorf("app: write upstream material %s: %w", path, err)
	}
	readBack, err := os.ReadFile(path) // #nosec G304 -- the path just written above.
	if err != nil {
		return fmt.Errorf("app: verify upstream material %s: %w", path, err)
	}
	if !bytes.Equal(readBack, body) {
		return fmt.Errorf("app: upstream material %s did not retain what was written", path)
	}
	return nil
}

var _ UpstreamKeyStore = FileUpstreamKeyStore{}

// kubernetesUpstreamKeyStore is the in-cluster store: the two chart-owned
// Secrets, written by a forced server-side apply and read back. It is a thin
// adapter over the methods that already existed, so the in-cluster behaviour
// is byte for byte what it was before the seam.
type kubernetesUpstreamKeyStore struct {
	bootstrap UpstreamSSHBootstrap
	client    kubernetesInterface
}

func (store kubernetesUpstreamKeyStore) Describe() string {
	return "Secrets " + store.bootstrap.Namespace + "/" + store.bootstrap.PrivateKeySecret +
		" and " + store.bootstrap.Namespace + "/" + store.bootstrap.KnownHostsSecret
}

func (store kubernetesUpstreamKeyStore) ActivationNote() string { return "" }

func (store kubernetesUpstreamKeyStore) Load(ctx context.Context) (secretMaterial, error) {
	return store.bootstrap.loadPersisted(ctx, store.client)
}

func (store kubernetesUpstreamKeyStore) SaveIdentity(ctx context.Context, privateKey, publicKey []byte) error {
	data := map[string][]byte{
		store.bootstrap.PrivateKeyDataKey: privateKey,
		store.bootstrap.PublicKeyDataKey:  publicKey,
	}
	if err := store.bootstrap.applySecret(ctx, store.client, store.bootstrap.PrivateKeySecret,
		data, store.bootstrap.KeyFieldManager); err != nil {
		return err
	}
	return store.bootstrap.verifySecretData(ctx, store.client, store.bootstrap.PrivateKeySecret, data)
}

func (store kubernetesUpstreamKeyStore) SaveKnownHosts(ctx context.Context, knownHosts []byte) error {
	data := map[string][]byte{store.bootstrap.KnownHostsDataKey: knownHosts}
	if err := store.bootstrap.applySecret(ctx, store.client, store.bootstrap.KnownHostsSecret,
		data, bootstrapFieldManager); err != nil {
		return err
	}
	return store.bootstrap.verifySecretData(ctx, store.client, store.bootstrap.KnownHostsSecret, data)
}

var _ UpstreamKeyStore = kubernetesUpstreamKeyStore{}
