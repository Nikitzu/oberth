package localinstall

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestEnsureMaterialCreatesEverythingTheServerReads(t *testing.T) {
	layout := NewLayout(t.TempDir())
	created, err := EnsureMaterial(layout, time.Now())
	if err != nil {
		t.Fatalf("EnsureMaterial: %v", err)
	}
	if len(created) == 0 {
		t.Fatal("a fresh root created nothing")
	}
	for _, path := range []string{layout.TLSCert, layout.TLSKey, layout.SSHHostKey,
		layout.Upstream, layout.ClientKey, layout.ClientKey + ".pub", layout.KnownHosts} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s is mode %o and holds private material", path, info.Mode().Perm())
		}
	}
}

// The certificate has to carry both spellings a client on this machine uses,
// or a client that trusts it still cannot connect.
func TestCertificateNamesLocalhostAndTheLoopbackAddress(t *testing.T) {
	layout := NewLayout(t.TempDir())
	if _, err := EnsureMaterial(layout, time.Now()); err != nil {
		t.Fatalf("EnsureMaterial: %v", err)
	}
	body, err := os.ReadFile(layout.TLSCert)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	block, _ := pem.Decode(body)
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := certificate.VerifyHostname("localhost"); err != nil {
		t.Fatalf("certificate does not name localhost: %v", err)
	}
	loopback := false
	for _, address := range certificate.IPAddresses {
		if address.Equal(net.ParseIP("127.0.0.1")) {
			loopback = true
		}
	}
	if !loopback {
		t.Fatal("certificate carries no 127.0.0.1 SAN, so a client reaching it by address fails the handshake")
	}
}

// The SSH keys have to be real SSH keys, and the public half has to be the
// private half's, or the uplink registers a fingerprint nothing can present.
func TestGeneratedSSHKeysAreUsableAndMatched(t *testing.T) {
	layout := NewLayout(t.TempDir())
	if _, err := EnsureMaterial(layout, time.Now()); err != nil {
		t.Fatalf("EnsureMaterial: %v", err)
	}
	private, err := os.ReadFile(layout.ClientKey)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(private)
	if err != nil {
		t.Fatalf("parse private key: %v", err)
	}
	public, err := os.ReadFile(layout.ClientKey + ".pub")
	if err != nil {
		t.Fatalf("read public: %v", err)
	}
	parsed, _, _, _, err := ssh.ParseAuthorizedKey(public)
	if err != nil {
		t.Fatalf("parse public key: %v", err)
	}
	if ssh.FingerprintSHA256(parsed) != ssh.FingerprintSHA256(signer.PublicKey()) {
		t.Fatal("the published public key is not the private key's own")
	}
}

// Re-running must change nothing, or a second install invalidates the uplink
// the first one registered and every client configured against it.
func TestEnsureMaterialIsIdempotent(t *testing.T) {
	layout := NewLayout(t.TempDir())
	if _, err := EnsureMaterial(layout, time.Now()); err != nil {
		t.Fatalf("first: %v", err)
	}
	before := map[string][]byte{}
	for _, path := range []string{layout.TLSCert, layout.TLSKey, layout.SSHHostKey, layout.ClientKey} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		before[path] = body
	}
	created, err := EnsureMaterial(layout, time.Now())
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(created) != 0 {
		t.Fatalf("a re-run created %v", created)
	}
	for path, body := range before {
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if string(after) != string(body) {
			t.Fatalf("a re-run replaced %s", path)
		}
	}
}

func TestLaunchdAgentCarriesTheWholeServeCommand(t *testing.T) {
	layout := NewLayout(filepath.Join(t.TempDir(), "root"))
	plist, err := RenderLaunchdAgent("/usr/local/bin/oberth",
		[]string{"serve", "--engine=docker", "--data=" + layout.Data}, layout, "/usr/local/bin:/usr/bin:/bin")
	if err != nil {
		t.Fatalf("RenderLaunchdAgent: %v", err)
	}
	text := string(plist)
	for _, expected := range []string{
		"<string>" + LaunchdLabel + "</string>",
		"<string>/usr/local/bin/oberth</string>",
		"<string>serve</string>",
		"<string>--engine=docker</string>",
		"<key>KeepAlive</key>",
		"<string>" + layout.Logs + "</string>",
		// Without this the agent runs with launchd's minimal PATH, which does
		// not include /usr/local/bin, and the server refuses every push with
		// "docker is not on PATH".
		"<key>PATH</key>",
		"<string>/usr/local/bin:/usr/bin:/bin</string>",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("plist missing %q:\n%s", expected, text)
		}
	}
}

// A path that cannot be represented is a mistake to report, not to encode.
func TestLaunchdAgentRefusesAnUnrepresentablePath(t *testing.T) {
	layout := NewLayout("/tmp/root")
	if _, err := RenderLaunchdAgent("/usr/local/bin/ob\nerth", nil, layout, ""); err == nil {
		t.Fatal("a binary path with a newline was accepted")
	}
}

func TestPushBannerNamesTheOneCommandThatMattersAndTheSetupItNeeds(t *testing.T) {
	banner := PushBanner("https://localhost:8443", "127.0.0.1", 8022, "/root/ssh/client_key", nil)
	for _, expected := range []string{
		"https://localhost:8443", "ssh://127.0.0.1:8022",
		"git push oberth HEAD", "oberth init",
		"upstream add --engine=docker", "secretstore init --engine=docker",
		"/root/ssh/client_key",
	} {
		if !strings.Contains(banner, expected) {
			t.Fatalf("banner missing %q:\n%s", expected, banner)
		}
	}
}

// Safari, Chrome, Firefox and macOS curl do not accept an Ed25519 key in a
// TLS handshake, so the dashboard was unreachable from a browser while every
// Go client worked. ECDSA P-256 is accepted by all of them.
func TestCertificatesAreECDSAAndAnEd25519OneIsReplaced(t *testing.T) {
	layout := NewLayout(t.TempDir())
	if _, err := EnsureMaterial(layout, time.Now()); err != nil {
		t.Fatalf("EnsureMaterial: %v", err)
	}
	if algorithm := certificateAlgorithm(t, layout.TLSCert); algorithm != x509.ECDSA {
		t.Fatalf("issued a %s certificate, want ECDSA", algorithm)
	}

	writeEd25519Certificate(t, layout.TLSCert, layout.TLSKey)
	created, err := EnsureMaterial(layout, time.Now())
	if err != nil {
		t.Fatalf("EnsureMaterial: %v", err)
	}
	if algorithm := certificateAlgorithm(t, layout.TLSCert); algorithm != x509.ECDSA {
		t.Fatalf("an Ed25519 certificate was kept: %s", algorithm)
	}
	if len(created) != 1 || created[0] != layout.TLSCert {
		t.Fatalf("the replacement was not reported: %v", created)
	}

	storeCert := filepath.Join(layout.Root, "openbao-tls", "tls.crt")
	storeKey := filepath.Join(layout.Root, "openbao-tls", "tls.key")
	writeEd25519Certificate(t, storeCert, storeKey)
	made, err := EnsureSelfSignedCertificate(storeCert, storeKey, "oberth-openbao", []string{"localhost"}, nil, time.Now())
	if err != nil || !made {
		t.Fatalf("EnsureSelfSignedCertificate over an Ed25519 certificate = (%v, %v), want a replacement", made, err)
	}
	if algorithm := certificateAlgorithm(t, storeCert); algorithm != x509.ECDSA {
		t.Fatalf("the store's Ed25519 certificate was kept: %s", algorithm)
	}
	made, err = EnsureSelfSignedCertificate(storeCert, storeKey, "oberth-openbao", []string{"localhost"}, nil, time.Now())
	if err != nil || made {
		t.Fatalf("an ECDSA certificate was reissued: (%v, %v)", made, err)
	}
}

func certificateAlgorithm(t *testing.T, path string) x509.PublicKeyAlgorithm {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(body)
	if block == nil {
		t.Fatalf("%s holds no PEM certificate", path)
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return certificate.PublicKeyAlgorithm
}

func writeEd25519Certificate(t *testing.T, certPath, keyPath string) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
}
