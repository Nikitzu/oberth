package periapsis

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// MaxRepoCacheNameLength bounds the readable half of a cache directory name.
// The digest that follows it is what actually keeps two repositories apart.
const MaxRepoCacheNameLength = 48

// RepoCacheSegment derives the single name a repository's cross-run build
// cache lives under, inside its tier's root.
//
// It lives here rather than in one engine because both engines have to agree:
// the Argo engine uses it as a node directory under the tier's host path, and
// the Docker engine uses it as the name of a per-repository volume. Two
// implementations of "which cache is this repository's" is two chances for one
// repository to read another's.
//
// The digest key is the org-qualified identity (org/repo), so same-name repos
// under different upstreams land in different caches. Upstream org uniqueness
// is enforced at registration time, making org/repo sufficient for isolation.
//
// The repository name arrives from a push, so it is never used verbatim in a
// path. Only lowercase alphanumerics, '-' and '_' survive; '.' and '/' are
// deliberately not in that set, which makes "." and ".." structurally
// unreachable rather than filtered for. The trailing digest is taken over the
// qualified form, so two repositories that sanitise or truncate to the same
// readable prefix still land in different places.
//
// NOTE: this changed in v0.14 from bare-name to org-qualified digesting.
// Existing build caches start cold after the change; old cache directories and
// volumes are orphaned but harmless.
func RepoCacheSegment(repo, upstreamOrg string) string {
	qualified := repo
	if upstreamOrg != "" {
		qualified = upstreamOrg + "/" + repo
	}
	digest := sha256.Sum256([]byte(qualified))
	var safe strings.Builder
	for _, character := range strings.ToLower(qualified) {
		switch {
		case character >= 'a' && character <= 'z',
			character >= '0' && character <= '9',
			character == '-', character == '_':
			safe.WriteRune(character)
		default:
			safe.WriteByte('-')
		}
	}
	readable := strings.Trim(safe.String(), "-_")
	if len(readable) > MaxRepoCacheNameLength {
		readable = strings.Trim(readable[:MaxRepoCacheNameLength], "-_")
	}
	if readable == "" {
		readable = "repo"
	}
	return readable + "-" + hex.EncodeToString(digest[:])[:12]
}
