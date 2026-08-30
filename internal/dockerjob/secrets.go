package dockerjob

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/oberthci/oberth/pkg/periapsis"
)

// The credentialed path, without Kubernetes.
//
// On a cluster a credentialed step gets a projected ServiceAccount token, a
// memory-backed emptyDir at /run/oberth-secrets, and VAULT_ADDR plus
// OBERTH_VAULT_ROLE; `oberth secretstore exec` then logs in with that token,
// writes the declared secrets into the emptyDir and execs the real command.
//
// Here the token is minted by the server and delivered on a per-run volume at
// the same path the projected one occupies, the emptyDir is a tmpfs, and the
// environment is identical. `oberth secretstore exec` is not modified: it
// still reads the conventional token path, still refuses a non-tmpfs
// directory, and still writes $OBERTH_SECRETSTORE_DIR/<secret>/<field>. A
// pipeline therefore cannot tell which engine ran it, which is the whole
// requirement.
const (
	// IdentityMountPath is where the run's minted identity is delivered. It is
	// the in-cluster projected ServiceAccount directory, deliberately: using
	// the same path is what lets the secret store client and `secretstore
	// exec` run byte for byte unchanged under both engines.
	IdentityMountPath = "/var/run/secrets/kubernetes.io/serviceaccount" // #nosec G101 -- a mount path, not a credential.
	// IdentityTokenName is the file inside it.
	IdentityTokenName = "token" // #nosec G101 -- a file name, not a credential.
	// IdentityCAName is the store's trust anchor, delivered beside the token
	// on the same read-only volume. The path travels as VAULT_CACERT, never
	// the bytes, which is what the Argo path does with the cluster's own
	// anchor and for the same reason: the server states what to trust.
	IdentityCAName = "vault-ca.crt"
	// SecretsMountPath is the tmpfs a credentialed step materialises secrets
	// into. It is internal/argojob.SecretsMountPath, and it has to be, because
	// the pipeline's own `secretstore exec --dir=` names it.
	SecretsMountPath = "/run/oberth-secrets" // #nosec G101 -- a mount path, not a credential.
	// DefaultJWTAuthMount, DefaultCIRole and DefaultReleaseRole are the names
	// `oberth secretstore init --engine=docker` creates. The server defaults
	// to them so that the two commands agree by construction rather than by an
	// operator repeating the same three strings on the serve command line.
	DefaultJWTAuthMount = "jwt"
	DefaultCIRole       = "oberth-ci"
	DefaultReleaseRole  = "oberth-release"

	// secretsTmpfsBytes bounds the secret tmpfs. The Argo path's memory-backed
	// emptyDir carries a sizeLimit for the same reason.
	secretsTmpfsBytes = 16 << 20
)

// IdentityMinter signs a run's identity for the secret store.
//
// An interface rather than the concrete minter so this package does not depend
// on the secret store client, and so a test can assert what the engine does
// with an identity without holding a signing key.
type IdentityMinter interface {
	Mint(ctx context.Context, tierRole, org, repo, runID string) (string, error)
}

// SecretStoreConfig is everything the engine needs to hand a credentialed run
// its coordinates. An empty Address disables the credentialed path, and a
// pipeline declaring secret paths is then refused at submission rather than
// run without the credentials it asked for.
type SecretStoreConfig struct {
	Address string
	// KVMount is the KV v2 mount holding Oberth's secrets, advertised so the
	// virtual oberth/upstream/... form can be translated in the container.
	KVMount string
	// CIRole and ReleaseRole are the OpenBao jwt roles for the two tiers. Each
	// binds its own subject, which is what keeps the tiers apart.
	CIRole      string
	ReleaseRole string
	// AuthMount is the OpenBao auth mount a step logs in at. Empty selects
	// DefaultJWTAuthMount.
	AuthMount string
	Minter    IdentityMinter
	// ContainerAddress is the store as a step container reaches it, which is
	// not the loopback address the server uses: inside a container 127.0.0.1
	// is the container. Empty falls back to Address.
	ContainerAddress string
	// HostGatewayName, when set, is mapped to the daemon's host gateway on
	// every credentialed step, so ContainerAddress resolves.
	HostGatewayName string
	// CACertPEM is the store's trust anchor. The local store's certificate is
	// this machine's own, so there is nothing in the system pool that would
	// verify it.
	CACertPEM []byte
}

// Enabled reports whether a credentialed run can be served.
func (config SecretStoreConfig) Enabled() bool {
	return strings.TrimSpace(config.Address) != "" && config.Minter != nil
}

// roleFor selects the tier role, the same shape as the Argo engine's
// vaultRoleFor and for the same reason: one answer to "what tier is this run".
// Naming a role is advisory, exactly as it is on a cluster; the boundary is
// that the role's bound_subject accepts only the subject this server minted
// for that tier.
func (config SecretStoreConfig) roleFor(trigger periapsis.Trigger) string {
	switch trigger {
	case periapsis.TriggerCI:
		return strings.TrimSpace(config.CIRole)
	case periapsis.TriggerRelease:
		return strings.TrimSpace(config.ReleaseRole)
	default:
		return ""
	}
}

// credentialEnvironment is the coordinates a credentialed step receives. It is
// field for field what internal/argojob injects, so a pipeline's own
// `secretstore exec` invocation needs no edit.
func (config SecretStoreConfig) credentialEnvironment(trigger periapsis.Trigger) []string {
	mount := strings.TrimSpace(config.KVMount)
	if mount == "" {
		mount = "oberth"
	}
	address := strings.TrimSpace(config.ContainerAddress)
	if address == "" {
		address = strings.TrimSpace(config.Address)
	}
	authMount := strings.TrimSpace(config.AuthMount)
	if authMount == "" {
		authMount = DefaultJWTAuthMount
	}
	environment := []string{
		"VAULT_ADDR=" + address,
		"OBERTH_VAULT_ROLE=" + config.roleFor(trigger),
		// The mount, because off-cluster the identity is a signed subject and
		// the method that validates it is jwt, not kubernetes. The client
		// takes the identical login payload at either mount.
		"OBERTH_VAULT_AUTH_MOUNT=" + authMount,
		"OBERTH_SECRETSTORE_KV_MOUNT=" + mount,
	}
	if len(config.CACertPEM) != 0 {
		environment = append(environment, "VAULT_CACERT="+IdentityMountPath+"/"+IdentityCAName)
	}
	return environment
}

// mintIdentity produces the run's token, refusing rather than running a
// credentialed pipeline with no identity to log in with.
func (config SecretStoreConfig) mintIdentity(ctx context.Context, request Request) (string, error) {
	if !config.Enabled() {
		return "", errors.New("dockerjob: no secret store is configured, so a credentialed pipeline cannot run")
	}
	role := config.roleFor(request.Trigger)
	if role == "" {
		return "", fmt.Errorf("dockerjob: no secret store role is configured for the %q tier", request.Trigger)
	}
	token, err := config.Minter.Mint(ctx, role, request.Org, request.Repo, request.RunID)
	if err != nil {
		return "", fmt.Errorf("dockerjob: mint the run identity: %w", err)
	}
	return token, nil
}

// ContainerStoreAddress rewrites a store address for use inside a step
// container, returning the rewritten address and the host name that must be
// mapped to the daemon's gateway for it to resolve.
//
// A loopback address is the case that matters: the server reaches the store at
// 127.0.0.1 because that is where it is published, and inside a container that
// same address names the container. Anything else is left alone, because an
// address that already names a reachable host is one the operator chose.
func ContainerStoreAddress(address, gatewayName string) (string, string) {
	parsed, err := url.Parse(strings.TrimSpace(address))
	if err != nil || parsed.Host == "" {
		return address, ""
	}
	host := parsed.Hostname()
	if host != "127.0.0.1" && host != "localhost" && host != "::1" && host != "[::1]" {
		return address, ""
	}
	port := parsed.Port()
	parsed.Host = gatewayName
	if port != "" {
		parsed.Host = gatewayName + ":" + port
	}
	return parsed.String(), gatewayName
}
