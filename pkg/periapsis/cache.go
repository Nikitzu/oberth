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
// The repository name arrives from a push, so it is never used verbatim in a
// path. Only lowercase alphanumerics, '-' and '_' survive; '.' is deliberately
// not in that set, which makes "." and ".." structurally unreachable rather
// than filtered for. The trailing digest is taken over the original name, so
// two repositories that sanitise or truncate to the same readable prefix still
// land in different places.
func RepoCacheSegment(repo string) string {
	digest := sha256.Sum256([]byte(repo))
	var safe strings.Builder
	for _, character := range strings.ToLower(repo) {
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
