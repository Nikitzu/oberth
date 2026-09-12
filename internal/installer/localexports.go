package installer

import (
	"context"
	"os"
)

// Exports for the clusterless install.
//
// `oberth install --engine=docker` writes the same client configuration, in
// the same place, holding the same token command, and stores the bearer token
// in the same platform secret store under the same service name. It reaches
// those decisions through these wrappers rather than repeating them, because
// two answers to "where does the CLI read its configuration" is a machine that
// works after one install and not the other.

// ClientConfigRoot is the directory both installs write the CLI and MCP
// configuration into.
func ClientConfigRoot() (string, error) { return clientConfigRoot() }

// TokenCommandForHost returns the command whose output is the bearer token,
// and the command that puts it there.
func TokenCommandForHost(profile string) (read string, store string) {
	return tokenCommandForHost(profile)
}

// RenderClientEnv is the env file the CLI sources. It deliberately holds no
// token: OBERTH_TOKEN_COMMAND names a command that reads one.
func RenderClientEnv(baseURL, caPath, tokenCommand string) string {
	return renderClientEnv(baseURL, caPath, tokenCommand)
}

// RenderMCPConfig is the MCP client configuration.
func RenderMCPConfig(baseURL, tokenCommand string) ([]byte, error) {
	return renderMCPConfig(baseURL, tokenCommand)
}

// StoreUplinkToken puts the bearer token in the platform secret store, under
// the same service name the cluster install uses, so a machine that has had
// either install answers OBERTH_TOKEN_COMMAND.
func StoreUplinkToken(ctx context.Context, profile, token string) error {
	return storeUplinkTokenFor(ctx, Deps{}, profile, token)
}

// DisplayPath shortens a path under the home directory for operator output.
func DisplayPath(path string) string { return displayPath(path) }

// AtomicWriteFile writes a file through a temporary and a rename.
func AtomicWriteFile(path string, body []byte, mode os.FileMode) error {
	return atomicWriteFile(path, body, mode)
}

// ShellProfilePath is the profile file the login shell reads, for the
// clusterless install's --shell-profile yes.
func ShellProfilePath(shell string) (string, error) { return shellProfilePath(Deps{}, shell) }

// AdoptUplinkToken copies a pre-profile token into the profile's own entry.
func AdoptUplinkToken(ctx context.Context, profile string) (bool, error) {
	return adoptUplinkTokenFor(ctx, Deps{}, profile)
}
