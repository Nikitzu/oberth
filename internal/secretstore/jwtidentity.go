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
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/oberthci/oberth/pkg/periapsis"
)

// A local run identity, for deployments with no Kubernetes.
//
// In a cluster the kubelet mints the ServiceAccount token a step logs in with,
// and OpenBao's kubernetes auth method binds a role to a ServiceAccount name.
// Off-cluster there is no kubelet, so the server mints a short-lived JWT
// instead and OpenBao's jwt auth method binds a role to the subject claim.
// Everything else is identical: the same client, the same login payload, the
// same KV paths, and the same policy engine deciding what the identity may
// read. Tier separation therefore stays where it is on a cluster, inside
// OpenBao, rather than becoming a property of Oberth's own code.
//
// What genuinely degrades is stated in docs/docker-engine-secrets.md and is
// worth repeating at the definition: the server holds the signing key, so a
// compromised server process can mint either tier's subject. In a cluster it
// cannot, because it cannot forge a kubelet-issued token. OpenBao still
// enforces the policy for whoever it believes is asking; the weaker claim is
// about who it can be persuaded to believe.

const (
	// RunIdentityTTL bounds a minted run identity. The in-cluster projected
	// ServiceAccount token expires in 600 seconds, so this is parity rather
	// than a new number. A jwt login cannot be revoked before expiry the way a
	// TokenReview-backed one can, which is exactly why it is short.
	RunIdentityTTL = 10 * time.Minute

	// RunIdentityAudience is the audience every minted identity carries, and
	// the one the jwt role binds. It stops a token minted for this server
	// being replayed against another mount that happens to trust the same key.
	RunIdentityAudience = "oberth"

	// maxSigningKeyBytes bounds the key file read.
	maxSigningKeyBytes = 1 << 20
)

// JWTMinter signs short-lived run identities with a local RSA key.
type JWTMinter struct {
	key      *rsa.PrivateKey
	audience string
	ttl      time.Duration
	now      func() time.Time
}

// NewJWTMinter loads a PEM-encoded RSA private key from disk.
//
// RSA rather than an elliptic curve because OpenBao's jwt auth method takes
// the public half as jwt_validation_pubkeys, and RS256 is the algorithm every
// version of it accepts without further configuration.
func NewJWTMinter(path string) (*JWTMinter, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, errors.New("secret store: a JWT signing key path is required to mint run identities")
	}
	info, err := os.Stat(trimmed)
	if err != nil {
		return nil, fmt.Errorf("secret store: read JWT signing key: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("secret store: JWT signing key %s is readable by group or other (mode %o); "+
			"it mints run identities and must be 0600", trimmed, info.Mode().Perm())
	}
	if info.Size() > maxSigningKeyBytes {
		return nil, fmt.Errorf("secret store: JWT signing key %s is larger than %d bytes", trimmed, maxSigningKeyBytes)
	}
	body, err := os.ReadFile(trimmed) // #nosec G304 -- an operator-supplied serve flag, stat-checked above.
	if err != nil {
		return nil, fmt.Errorf("secret store: read JWT signing key: %w", err)
	}
	key, err := parseRSAPrivateKey(body)
	if err != nil {
		return nil, err
	}
	return &JWTMinter{key: key, audience: RunIdentityAudience, ttl: RunIdentityTTL, now: time.Now}, nil
}

func parseRSAPrivateKey(body []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(body)
	if block == nil {
		return nil, errors.New("secret store: JWT signing key is not PEM encoded")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		parsed, pkcs8Err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if pkcs8Err != nil {
			return nil, fmt.Errorf("secret store: parse JWT signing key: %w", pkcs8Err)
		}
		rsaKey, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("secret store: JWT signing key is %T, and OpenBao's jwt auth method needs an RSA key", parsed)
		}
		key = rsaKey
	}
	if key.N.BitLen() < 2048 {
		return nil, fmt.Errorf("secret store: JWT signing key is %d bits, below the 2048 minimum", key.N.BitLen())
	}
	return key, nil
}

// RunSubject is the subject claim a run's identity carries, and therefore the
// value the jwt role binds with bound_subject. Today it is the tier's role
// name and nothing else, which is the exact analogue of a kubernetes role
// binding one ServiceAccount name.
//
// It takes org and repo because the next step is per-repository identities.
// Upstream v0.13.31 gives a grant-holding repository its own ServiceAccount
// and its own Vault role scoped to <kv>/data/upstream/<org>/<repo>/*, and the
// same move here is to return "<tier>-<org>-<repo>" and have `secretstore init`
// write one jwt role per repository bound to that subject. Nothing else in the
// chain changes: the minting call site already knows the org and the repo, the
// policy is generated the same way, and the client is untouched. The reason it
// is not done yet is that a per-repository role has to be created when the
// grant is created, and the docker engine has no grant reconciler.
func RunSubject(tierRole, org, repo string) string {
	_ = org
	_ = repo
	return strings.TrimSpace(tierRole)
}

// Mint returns a signed identity for one run of one repository at one tier.
//
// The subject comes from the tier the durable run carries, never from the
// pipeline document: a repository cannot ask to be a different identity, it
// can only ask for paths, which admission then scopes to what its own tier and
// its own org and repo may reach.
func (minter *JWTMinter) Mint(_ context.Context, tierRole, org, repo, runID string) (string, error) {
	subject := RunSubject(tierRole, org, repo)
	if subject == "" {
		return "", errors.New("secret store: a run identity needs a tier role as its subject")
	}
	// The subject is a single OpenBao role-binding value, so it may not carry
	// a separator or whitespace: bound_subject is compared literally, and a
	// subject that is not what it looks like is a boundary that is not what it
	// looks like.
	if strings.ContainsAny(subject, "\x00\r\n\t /") || strings.TrimSpace(subject) != subject {
		return "", fmt.Errorf("secret store: run identity subject %q must be a single clean segment", subject)
	}
	if err := periapsis.ValidateSecretStorePath(subject); err != nil {
		return "", fmt.Errorf("secret store: run identity subject %q: %w", subject, err)
	}
	issued := minter.now().UTC()
	claims := map[string]any{
		"iss": "oberth",
		"sub": subject,
		"aud": minter.audience,
		"iat": issued.Unix(),
		"nbf": issued.Add(-30 * time.Second).Unix(),
		"exp": issued.Add(minter.ttl).Unix(),
		// Not a claim OpenBao binds. It is here so an audit device entry can
		// be tied back to the run that caused it.
		"oberth_run": runID,
	}
	return minter.sign(claims)
}

// sign produces a compact RS256 JWS. Minting is the trivial direction of JWT
// handling: there is no untrusted input, no algorithm to negotiate and nothing
// to parse, which is why it is done here rather than by taking on a dependency
// whose value is in the verification path this code never walks.
func (minter *JWTMinter) sign(claims map[string]any) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encoding := base64.RawURLEncoding
	signing := encoding.EncodeToString(header) + "." + encoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signing))
	signature, err := rsa.SignPKCS1v15(rand.Reader, minter.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("secret store: sign run identity: %w", err)
	}
	return signing + "." + encoding.EncodeToString(signature), nil
}

// PublicKeyPEM is the half OpenBao holds, written as jwt_validation_pubkeys.
func (minter *JWTMinter) PublicKeyPEM() ([]byte, error) {
	encoded, err := x509.MarshalPKIXPublicKey(&minter.key.PublicKey)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: encoded}), nil
}
