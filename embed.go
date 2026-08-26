// Package oberth exposes repository-level assets that ship inside the Oberth
// binaries. Embedding keeps exactly one canonical copy of each asset: the
// repository file is the source, and the digest-pinned, signed container image
// carries the same bytes, so an administrator can extract them over a channel
// whose integrity is already proven by image signature verification.
package oberth

import (
	"embed"
)

// SetupSecretStoreScript is the canonical scripts/setup-secretstore.sh: the
// one command an administrator runs NEXT TO their OpenBao (or Vault) to make
// the store trust Oberth's ServiceAccount. It is embedded so
// `oberth secretstore setup --print-script` can hand the administrator the
// exact reviewed bytes from the signed image, instead of asking them to trust
// a separate download channel.
//
// The Oberth server binary deliberately has no code path that accepts a
// secret-store admin token: store-side configuration always runs where the
// administrator's own CLI session already holds that authority.
//
//go:embed scripts/setup-secretstore.sh
var SetupSecretStoreScript []byte

// ChartFS is the Oberth Helm chart, carried inside the binary that installs it.
//
// Without this an install needs the chart from somewhere else, and there is
// nowhere good: the published oberth-charts index only ever holds upstream's
// releases, so a fork build asking for its own version got "chart oberth
// matching <tag> not found" after it had already created the cluster. Handing
// the operator a .tgz to download and name with --chart works, but makes the
// install a three-file exercise where the chart and the binary can drift.
//
// The binary knows its own version, so it should carry the chart that matches
// it. --chart still overrides, for local iteration on the chart itself.
//
//go:embed all:charts/oberth
var ChartFS embed.FS
