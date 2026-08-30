package dockerjob

import (
	"context"
	"errors"
	"fmt"
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
	Minter      IdentityMinter
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
	return []string{
		"VAULT_ADDR=" + strings.TrimSpace(config.Address),
		"OBERTH_VAULT_ROLE=" + config.roleFor(trigger),
		"OBERTH_SECRETSTORE_KV_MOUNT=" + mount,
	}
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
	org, repo := splitOrgRepo(request.Repo)
	token, err := config.Minter.Mint(ctx, role, org, repo, request.RunID)
	if err != nil {
		return "", fmt.Errorf("dockerjob: mint the run identity: %w", err)
	}
	return token, nil
}

// splitOrgRepo separates "org/name". It exists so the org and the repo reach
// the minter, which is what a per-repository subject will need.
func splitOrgRepo(repo string) (string, string) {
	org, name, found := strings.Cut(strings.TrimSpace(repo), "/")
	if !found {
		return "", org
	}
	return org, name
}
