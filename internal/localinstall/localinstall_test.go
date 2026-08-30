package localinstall

import (
	"crypto/x509"
	"encoding/pem"
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
