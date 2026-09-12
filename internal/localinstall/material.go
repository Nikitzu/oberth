// Package localinstall provisions everything a clusterless Oberth server needs
// on one machine: a data directory, a TLS identity for localhost, the SSH host
// and upstream keys, and the developer's own push identity.
//
// It is the counterpart of the Helm chart. The chart's job is to hand the
// server a set of files through projected volumes; here the same files are
// created directly, in one directory the operator owns, with the same names
// the serve flags default to reading. Everything is create-if-absent, so a
// second `oberth install --engine=docker` finds what the first one made and
// changes nothing.
package localinstall

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Layout is where every file the server reads lives, derived from one root.
//
// One root rather than the chart's several mount points, because there is no
// projection layer to keep them apart and pretending otherwise would only make
// the paths longer.
type Layout struct {
	Root       string
	Data       string
	Database   string
	TLSCert    string
	TLSKey     string
	SSHHostKey string
	Upstream   string
	KnownHosts string
	ClientKey  string
	// ClientKnownHosts pins this server's own host key for the client side,
	// so a push over the oberth remote verifies without a first-use prompt.
	ClientKnownHosts string
	Logs             string
	SigningKey       string
}

// NewLayout derives every path from the install root.
func NewLayout(root string) Layout {
	return Layout{
		Root:             root,
		Data:             filepath.Join(root, "data"),
		Database:         filepath.Join(root, "data", "oberth.sqlite"),
		TLSCert:          filepath.Join(root, "tls", "tls.crt"),
		TLSKey:           filepath.Join(root, "tls", "tls.key"),
		SSHHostKey:       filepath.Join(root, "ssh", "ssh_host_key"),
		Upstream:         filepath.Join(root, "ssh", "upstream_key"),
		KnownHosts:       filepath.Join(root, "ssh", "known_hosts"),
		ClientKey:        filepath.Join(root, "ssh", "client_key"),
		ClientKnownHosts: filepath.Join(root, "ssh", "client_known_hosts"),
		Logs:             filepath.Join(root, "server.log"),
		SigningKey:       filepath.Join(root, "jwt-signing.pem"),
	}
}

// certificateLifetime is deliberately long. This certificate is trusted by one
// developer's own clients on one machine, and an expiry that lands mid-project
// presents as a client that cannot reach a server that is running.
const certificateLifetime = 10 * 365 * 24 * time.Hour

// EnsureMaterial creates every file the server needs that does not already
// exist, and reports what it made.
func EnsureMaterial(layout Layout, now time.Time) ([]string, error) {
	var created []string
	for _, directory := range []string{layout.Root, layout.Data,
		filepath.Dir(layout.TLSCert), filepath.Dir(layout.SSHHostKey)} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, fmt.Errorf("localinstall: create %s: %w", directory, err)
		}
	}
	madeTLS, err := ensureTLS(layout, now)
	if err != nil {
		return nil, err
	}
	if madeTLS {
		created = append(created, layout.TLSCert)
	}
	for _, key := range []string{layout.SSHHostKey, layout.Upstream, layout.ClientKey} {
		made, err := ensureSSHKey(key)
		if err != nil {
			return nil, err
		}
		if made {
			created = append(created, key)
		}
	}
	if _, err := os.Stat(layout.KnownHosts); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(layout.KnownHosts, nil, 0o600); err != nil {
			return nil, fmt.Errorf("localinstall: create %s: %w", layout.KnownHosts, err)
		}
		created = append(created, layout.KnownHosts)
	} else if err != nil {
		return nil, fmt.Errorf("localinstall: read %s: %w", layout.KnownHosts, err)
	}
	return created, nil
}

// ensureTLS issues a self-signed certificate for localhost.
//
// Self-signed rather than a CA and a leaf: there is exactly one server and one
// set of clients, both on this machine, so a chain adds a file to distribute
// and buys nothing. The clients are pointed at this certificate as their trust
// anchor through OBERTH_CA_CERT, which is the same mechanism the cluster
// install uses for its own private signer.
func ensureTLS(layout Layout, now time.Time) (bool, error) {
	return EnsureSelfSignedCertificate(layout.TLSCert, layout.TLSKey, "localhost",
		[]string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}, now)
}

// EnsureSelfSignedCertificate issues a certificate if one is not already
// there, or if the one there is one no browser will accept, and reports
// whether it wrote one.
func EnsureSelfSignedCertificate(certPath, keyPath, commonName string, names []string, addresses []net.IP, now time.Time) (bool, error) {
	if _, err := os.Stat(certPath); err == nil {
		if _, keyErr := os.Stat(keyPath); keyErr == nil {
			unusable, err := CertificateIsEd25519(certPath)
			if err != nil {
				return false, err
			}
			if !unusable {
				return false, nil
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("localinstall: read %s: %w", certPath, err)
	}
	return true, IssueSelfSignedCertificate(certPath, keyPath, commonName, names, addresses, now)
}

// CertificateIsEd25519 reports whether the certificate at path carries an
// Ed25519 key. Earlier installs issued those, and Safari, Chrome, Firefox and
// the macOS curl all refuse Ed25519 in a TLS handshake, so the dashboard was
// unreachable from a browser while every Go client worked and nobody noticed.
// Such a certificate is replaced on the next install run.
func CertificateIsEd25519(path string) (bool, error) {
	body, err := os.ReadFile(path) // #nosec G304 -- the certificate this install manages.
	if err != nil {
		return false, fmt.Errorf("localinstall: read %s: %w", path, err)
	}
	block, _ := pem.Decode(body)
	if block == nil {
		return false, fmt.Errorf("localinstall: %s holds no PEM certificate", path)
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false, fmt.Errorf("localinstall: parse %s: %w", path, err)
	}
	return certificate.PublicKeyAlgorithm == x509.Ed25519, nil
}

// IssueSelfSignedCertificate writes a certificate and its key.
//
// Self-signed rather than a CA and a leaf: every consumer is on this machine
// and is handed this exact file as its trust anchor, so a chain adds a file to
// distribute and buys nothing. It carries every name and address a consumer
// might use, because a client that trusts the certificate and reaches the
// server by a name it does not carry still fails the handshake, and that
// failure reads as "server unreachable".
func IssueSelfSignedCertificate(certPath, keyPath, commonName string, names []string, addresses []net.IP, now time.Time) error {
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return fmt.Errorf("localinstall: create %s: %w", filepath.Dir(certPath), err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return fmt.Errorf("localinstall: create %s: %w", filepath.Dir(keyPath), err)
	}
	// ECDSA P-256, not Ed25519: every browser and the platform TLS stacks
	// accept it, and Ed25519 is exactly what they do not.
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("localinstall: generate the TLS key: %w", err)
	}
	public := &private.PublicKey
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return fmt.Errorf("localinstall: generate the certificate serial: %w", err)
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName, Organization: []string{"Oberth local"}},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(certificateLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              names,
		IPAddresses:           addresses,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		return fmt.Errorf("localinstall: create the TLS certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return fmt.Errorf("localinstall: encode the TLS key: %w", err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		return fmt.Errorf("localinstall: write %s: %w", certPath, err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return fmt.Errorf("localinstall: write %s: %w", keyPath, err)
	}
	return nil
}

// ensureSSHKey creates an ed25519 identity and its public half.
func ensureSSHKey(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("localinstall: read %s: %w", path, err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return false, fmt.Errorf("localinstall: generate %s: %w", path, err)
	}
	block, err := ssh.MarshalPrivateKey(private, "oberth")
	if err != nil {
		return false, fmt.Errorf("localinstall: encode %s: %w", path, err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return false, fmt.Errorf("localinstall: write %s: %w", path, err)
	}
	authorized, err := ssh.NewPublicKey(public)
	if err != nil {
		return false, fmt.Errorf("localinstall: derive the public half of %s: %w", path, err)
	}
	if err := os.WriteFile(path+".pub", ssh.MarshalAuthorizedKey(authorized), 0o600); err != nil {
		return false, fmt.Errorf("localinstall: write %s.pub: %w", path, err)
	}
	return true, nil
}

// WriteClientKnownHosts records the server's host key for the address clients
// push to, derived from the host key on disk rather than scanned over the
// network, so the client never has to answer a first-use prompt and never
// trusts anything but the key this install generated. Rewritten on every
// install so a changed port or key is picked up.
func WriteClientKnownHosts(layout Layout, host string, port int) error {
	body, err := os.ReadFile(layout.SSHHostKey) // #nosec G304 -- the host key this install manages.
	if err != nil {
		return fmt.Errorf("localinstall: read %s: %w", layout.SSHHostKey, err)
	}
	private, err := ssh.ParseRawPrivateKey(body)
	if err != nil {
		return fmt.Errorf("localinstall: parse %s: %w", layout.SSHHostKey, err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		return fmt.Errorf("localinstall: derive the public half of %s: %w", layout.SSHHostKey, err)
	}
	line := fmt.Sprintf("[%s]:%d %s", host, port, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))))
	if err := os.WriteFile(layout.ClientKnownHosts, []byte(line+"\n"), 0o600); err != nil {
		return fmt.Errorf("localinstall: write %s: %w", layout.ClientKnownHosts, err)
	}
	return nil
}

// ClientSSHCommand is the GIT_SSH_COMMAND a checkout pushes with. It is a
// wrapper rather than a bare ssh line because core.sshCommand applies to every
// remote in a repository: pinning the install's key and host file directly
// would break the checkout's pushes to its forge. The wrapper applies them
// only when the target is this server and runs plain ssh otherwise.
func ClientSSHCommand(layout Layout) string {
	return filepath.Join(filepath.Dir(layout.ClientKey), "git-ssh")
}

// WriteClientSSHWrapper writes the wrapper ClientSSHCommand names.
func WriteClientSSHWrapper(layout Layout, host string) error {
	script := fmt.Sprintf(`#!/bin/sh
# Written by oberth install. A push to this machine's Oberth server uses the
# key and the host file the install minted; every other host gets plain ssh.
for argument in "$@"; do
  case "$argument" in
    %s|*@%s) exec ssh -i %q -o IdentitiesOnly=yes -o UserKnownHostsFile=%q "$@" ;;
  esac
done
exec ssh "$@"
`, host, host, layout.ClientKey, layout.ClientKnownHosts)
	if err := os.WriteFile(ClientSSHCommand(layout), []byte(script), 0o700); err != nil { // #nosec G306 -- an executable the user runs.
		return fmt.Errorf("localinstall: write %s: %w", ClientSSHCommand(layout), err)
	}
	return nil
}

// RenderClientSSHEnv is the block the clusterless install appends to the
// client env: the SSH command a push to this server should run with. The
// cluster install has no counterpart, since its clients bring their own keys
// and the variable stays unset there, which is what onboard checks.
func RenderClientSSHEnv(layout Layout) string {
	return fmt.Sprintf(`
# A push to the oberth remote authenticates with the key this install minted
# and trusts only this server's host key. oberth onboard copies this into the
# repository's core.sshCommand, so plain git push works too.
export OBERTH_SSH_COMMAND=%q
`, ClientSSHCommand(layout))
}
