package secretstore

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeSigningKey(t *testing.T, bits int, mode os.FileMode) (string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	path := filepath.Join(t.TempDir(), "signing.pem")
	body := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(path, body, mode); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path, key
}

func decodeClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts", len(parts))
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(body, &claims); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return claims
}

// The signature has to verify against the public half OpenBao is given, with
// the algorithm the header names, or every login fails with an error that
// blames the role.
func TestMintedIdentityVerifiesAgainstThePublishedPublicKey(t *testing.T) {
	path, key := writeSigningKey(t, 2048, 0o600)
	minter, err := NewJWTMinter(path)
	if err != nil {
		t.Fatalf("NewJWTMinter: %v", err)
	}
	token, err := minter.Mint(context.Background(), "oberth-ci", "acme", "widget", "run-1")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	parts := strings.Split(token, ".")
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
	pemBody, err := minter.PublicKeyPEM()
	if err != nil {
		t.Fatalf("PublicKeyPEM: %v", err)
	}
	block, _ := pem.Decode(pemBody)
	if block == nil {
		t.Fatal("public key is not PEM encoded")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse public key: %v", err)
	}
	if parsed.(*rsa.PublicKey).N.Cmp(key.PublicKey.N) != 0 {
		t.Fatal("the published public key is not the signing key's own")
	}
}

// The subject is the tier and the audience is bound, because those two claims
// are the whole of what OpenBao's role checks.
func TestMintedIdentityCarriesTheTierSubjectAndABoundedLifetime(t *testing.T) {
	path, _ := writeSigningKey(t, 2048, 0o600)
	minter, err := NewJWTMinter(path)
	if err != nil {
		t.Fatalf("NewJWTMinter: %v", err)
	}
	token, err := minter.Mint(context.Background(), "oberth-release", "acme", "widget", "run-9")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	claims := decodeClaims(t, token)
	if claims["sub"] != "oberth-release" {
		t.Fatalf("subject: got %v", claims["sub"])
	}
	if claims["aud"] != RunIdentityAudience {
		t.Fatalf("audience: got %v", claims["aud"])
	}
	if claims["oberth_run"] != "run-9" {
		t.Fatalf("run claim: got %v", claims["oberth_run"])
	}
	lifetime := time.Duration(int64(claims["exp"].(float64))-int64(claims["iat"].(float64))) * time.Second
	if lifetime != RunIdentityTTL {
		t.Fatalf("lifetime: got %s, want %s", lifetime, RunIdentityTTL)
	}
}

// A signing key readable by anyone else on the machine mints either tier.
func TestSigningKeyMustNotBeGroupOrWorldReadable(t *testing.T) {
	path, _ := writeSigningKey(t, 2048, 0o644)
	if _, err := NewJWTMinter(path); err == nil {
		t.Fatal("a world-readable signing key was accepted")
	}
}

func TestSigningKeyMustBeLargeEnough(t *testing.T) {
	path, _ := writeSigningKey(t, 1024, 0o600)
	if _, err := NewJWTMinter(path); err == nil {
		t.Fatal("a 1024-bit signing key was accepted")
	}
}

// The subject may never come from the document, so a tier role that is not a
// clean path segment is a bug worth failing on rather than signing.
func TestMintRefusesAnUnusableSubject(t *testing.T) {
	path, _ := writeSigningKey(t, 2048, 0o600)
	minter, err := NewJWTMinter(path)
	if err != nil {
		t.Fatalf("NewJWTMinter: %v", err)
	}
	for _, subject := range []string{"", "oberth/ci", "oberth ci"} {
		if _, err := minter.Mint(context.Background(), subject, "acme", "widget", "run"); err == nil {
			t.Fatalf("subject %q was accepted", subject)
		}
	}
}
