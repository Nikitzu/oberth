package argojob

// Per-repo identity selection: when a repository has a per-repo Vault
// identity (SA + policy + role), the submission path selects it instead
// of the shared tier-wide identity. This scopes each repo's Vault access
// to its own namespace and its own approved grants.

import (
	"github.com/oberthci/oberth/pkg/argoworkflow"
	"github.com/oberthci/oberth/pkg/periapsis"
)

// PerRepoIdentityConfig describes a repository's per-repo Vault identity.
// The ServiceAccount name doubles as the Vault role name and policy name.
type PerRepoIdentityConfig struct {
	// ServiceAccountName is the per-repo SA in the pipeline namespace.
	// It is also the Vault role name and policy name.
	ServiceAccountName string
}

// canonicalRepoKey constructs the map key for PerRepoIdentities from
// the upstream name, org, and bare repo name. This is the same canonical
// "upstream/org/repo" form used for grant storage (#245 BLOCKER B).
func canonicalRepoKey(upstreamName, org, repo string) string {
	if upstreamName == "" || org == "" {
		return repo
	}
	return upstreamName + "/" + org + "/" + repo
}

// identityForPerRepo selects the per-repo identity for a credentialed run.
// It returns the per-repo identity if one exists for this repo; otherwise
// it returns false and the caller falls back to the shared tier identity.
func (config Config) identityForPerRepo(upstreamName, org, repo string) (argoworkflow.Identity, bool) {
	if config.PerRepoIdentities == nil {
		return argoworkflow.Identity{}, false
	}
	key := canonicalRepoKey(upstreamName, org, repo)
	perRepo, exists := config.PerRepoIdentities[key]
	if !exists || perRepo.ServiceAccountName == "" {
		return argoworkflow.Identity{}, false
	}
	return argoworkflow.Identity{
		Namespace:                    config.Namespace,
		ServiceAccountName:           perRepo.ServiceAccountName,
		ExecutorServiceAccountName:   config.ExecutorServiceAccount,
		AutomountServiceAccountToken: true,
	}, true
}

// vaultRoleForPerRepo returns the per-repo Vault role name if one exists.
// The per-repo SA name IS the role name (they share the same name by
// convention). Returns empty string if no per-repo identity exists.
func (config Config) vaultRoleForPerRepo(upstreamName, org, repo string) string {
	if config.PerRepoIdentities == nil {
		return ""
	}
	key := canonicalRepoKey(upstreamName, org, repo)
	perRepo, exists := config.PerRepoIdentities[key]
	if !exists {
		return ""
	}
	return perRepo.ServiceAccountName
}

// identityForWithRepo selects the ServiceAccount for a run, checking for
// per-repo identities first. Per-repo identities are used for both CI and
// release triggers when the repo has declared secret paths and a per-repo
// identity exists. Falls back to the shared tier identity when no per-repo
// identity is configured.
//
// For fragment runs, the caller must pass the HOST repo name (the repo that
// declared the pipeline), not the fragment source's repo. This ensures the
// fragment runs under the host's identity, because the host's pipeline is
// what declared the secrets and the host's approval table is what was checked.
func (config Config) identityForWithRepo(trigger periapsis.Trigger, hasSecretPaths bool, upstreamName, org, repo string) (argoworkflow.Identity, error) {
	if !hasSecretPaths {
		// No secrets: always the pipeline SA, regardless of per-repo config.
		return config.identityFor(trigger, false)
	}

	// Try per-repo identity first.
	if identity, ok := config.identityForPerRepo(upstreamName, org, repo); ok {
		return identity, nil
	}

	// Fall back to shared tier identity.
	return config.identityFor(trigger, true)
}

// vaultRoleForWithRepo selects the Vault role, checking for per-repo roles
// first. Falls back to the shared tier role.
func (config Config) vaultRoleForWithRepo(trigger periapsis.Trigger, upstreamName, org, repo string) string {
	if role := config.vaultRoleForPerRepo(upstreamName, org, repo); role != "" {
		return role
	}
	return config.vaultRoleFor(trigger)
}
