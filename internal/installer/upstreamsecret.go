package installer

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// upstreamTokenStoreSecret is the name the forge token is filed under in the
// secret store, inside the upstream org's own subtree.
//
// One well-known name, rather than one per repository, because the token is
// the org's: `oberth/upstream/<org>/github-token` is readable by every
// repository of that upstream and by nothing else, which is exactly the scope
// of a token that installs the org's private packages.
const upstreamTokenStoreSecret = "github-token" // #nosec G101 — a KV path segment, not a credential.

// declaredUpstreamTokenPath is the path a pipeline declares in its
// oberth.ci/secret-paths annotation. It is the hierarchical spelling, which
// release admission authorizes structurally against the pushing repository's
// upstream — no allowlist entry, no administrator step.
func declaredUpstreamTokenPath(org string) string {
	return defaultKVPrefix + "/upstream/" + org + "/" + upstreamTokenStoreSecret
}

// upstreamTokenAPIPath is the same secret as a KV v2 API path, which is what
// `bao write` takes.
func upstreamTokenAPIPath(org string) string {
	return defaultKVPrefix + "/data/upstream/" + org + "/" + upstreamTokenStoreSecret
}

// storeReachable reports whether this install still holds what it needs to
// write to the store it configured. A re-run against an already-initialized
// production store has no root token unless the operator supplied one, and
// that case must say so rather than silently seed nothing.
func storeReachable(store SecretStoreResult) bool {
	return store.client.pod != "" && store.client.run != nil && strings.TrimSpace(store.rootToken) != ""
}

// seedUpstreamToken writes the forge token into the upstream org's subtree of
// the secret store.
//
// This is the step that used to be a port-forward and a hand-typed
// `bao kv put`. The installer already holds the token (it just wrote it to the
// push Secret) and already knows the org (it just registered the upstream), so
// leaving it to the operator only meant every install shipped a store with
// nothing in it and a pipeline that could not install a private package.
//
// The value travels on stdin as a JSON body, never in argv: the Kubernetes API
// server records exec arguments verbatim in its audit log.
func seedUpstreamToken(ctx context.Context, store SecretStoreResult, org, token string) error {
	org = strings.TrimSpace(org)
	if org == "" {
		return errors.New("no upstream org to scope the token to")
	}
	if strings.TrimSpace(token) == "" {
		return errors.New("no token to store")
	}
	if !storeReachable(store) {
		return errors.New("no secret store to write to")
	}
	if err := store.client.writeJSON(ctx, store.rootToken, upstreamTokenAPIPath(org), map[string]any{
		"data": map[string]any{"token": token},
	}); err != nil {
		// The error names the path, never the payload.
		return fmt.Errorf("write %s: %w", upstreamTokenAPIPath(org), err)
	}
	return nil
}
