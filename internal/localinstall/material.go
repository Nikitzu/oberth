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
	"crypto/ed25519"
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
	Logs       string
	SigningKey string
}

// NewLayout derives every path from the install root.
func NewLayout(root string) Layout {
	return Layout{
		Root:       root,
		Data:       filepath.Join(root, "data"),
		Database:   filepath.Join(root, "data", "oberth.sqlite"),
		TLSCert:    filepath.Join(root, "tls", "tls.crt"),
		TLSKey:     filepath.Join(root, "tls", "tls.key"),
		SSHHostKey: filepath.Join(root, "ssh", "ssh_host_key"),
		Upstream:   filepath.Join(root, "ssh", "upstream_key"),
		KnownHosts: filepath.Join(root, "ssh", "known_hosts"),
		ClientKey:  filepath.Join(root, "ssh", "client_key"),
		Logs:       filepath.Join(root, "server.log"),
		SigningKey: filepath.Join(root, "jwt-signing.pem"),
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
	if _, err := os.Stat(layout.TLSCert); err == nil {
		if _, keyErr := os.Stat(layout.TLSKey); keyErr == nil {
			return false, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("localinstall: read %s: %w", layout.TLSCert, err)
	}
	return true, IssueSelfSignedCertificate(layout.TLSCert, layout.TLSKey, "localhost",
		[]string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}, now)
}

// EnsureSelfSignedCertificate issues a certificate only if one is not already
// there, and reports whether it made one.
func EnsureSelfSignedCertificate(certPath, keyPath, commonName string, names []string, addresses []net.IP, now time.Time) (bool, error) {
	if _, err := os.Stat(certPath); err == nil {
		if _, keyErr := os.Stat(keyPath); keyErr == nil {
			return false, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("localinstall: read %s: %w", certPath, err)
	}
	return true, IssueSelfSignedCertificate(certPath, keyPath, commonName, names, addresses, now)
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
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("localinstall: generate the TLS key: %w", err)
	}
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
