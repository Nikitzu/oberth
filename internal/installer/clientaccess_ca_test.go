package installer

import (
	"context"
	"runtime"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func tlsSecret(data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "oberth-tls", Namespace: DefaultNamespace},
		Data:       data,
	}
}

// A client has to trust the signer. Handing it the leaf instead fails with
// "unable to verify the first certificate": it is asked to trust a certificate
// whose issuer it still does not have. The chart generated a CA, signed with
// it, and kept only the leaf, so every TLS client failed no matter what it was
// pointed at.
func TestTheSignerIsWhatClientsAreGiven(t *testing.T) {
	t.Parallel()
	deps := Deps{KubeClient: fake.NewClientset(tlsSecret(map[string][]byte{
		"tls.crt": []byte("LEAF"),
		"tls.key": []byte("KEY"),
		"ca.crt":  []byte("SIGNER"),
	}))}
	body, err := serverCACertificate(context.Background(), Config{}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "SIGNER" {
		t.Fatalf("clients were given %q, want the signer", body)
	}
}

// A Secret written before the chart kept the signer, or supplied through
// tls.existingSecret where the certificate is its own root, still has to work.
func TestLeafIsUsedWhenThereIsNoSeparateSigner(t *testing.T) {
	t.Parallel()
	deps := Deps{KubeClient: fake.NewClientset(tlsSecret(map[string][]byte{
		"tls.crt": []byte("SELF-SIGNED"),
		"tls.key": []byte("KEY"),
	}))}
	body, err := serverCACertificate(context.Background(), Config{}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "SELF-SIGNED" {
		t.Fatalf("got %q", body)
	}
}

// The read and the store must name the same item. They did not: the store
// named the account and the read did not, so a Keychain holding more than one
// item under this service -- which repeated installs produce -- answered with
// whichever came first. That is a token from an earlier deployment, and an MCP
// client reads the resulting 401 as "this server wants OAuth" rather than as a
// bad credential.
func TestKeychainReadAndStoreNameTheSameItem(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Keychain is macOS only")
	}
	read, store := tokenCommandForHost()
	for _, command := range []string{read, store} {
		if !strings.Contains(command, `-a "$USER"`) {
			t.Errorf("command does not name the account, so it can match another item: %s", command)
		}
		if !strings.Contains(command, "-s oberth-token") {
			t.Errorf("command does not name the service: %s", command)
		}
	}
}
