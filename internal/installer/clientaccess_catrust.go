package installer

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Configuring an MCP client is not the same as making it able to reach the
// server. Claude Code runs on Node, and Node carries its own trust store
// rather than reading the platform's, so a private signer the operating system
// already trusts is still unknown to it: the entry this install just wrote
// fails at the TLS handshake and the client reports the server as unreachable.
//
// NODE_EXTRA_CA_CERTS is the variable Node reads for exactly this, and Claude
// Code passes the `env` object of its settings file through to the process, so
// the CA the install wrote can be named there once rather than exported by
// every shell that starts the client.

// nodeExtraCACerts is Node's own name for "trust this signer as well".
const nodeExtraCACerts = "NODE_EXTRA_CA_CERTS"

// errCATrustAlreadySet reports that the settings file already names a
// different certificate. Two deployments' CAs cannot both live in one
// variable, so the operator is told and nothing is touched: the other value is
// as likely to be the one they want as ours is.
var errCATrustAlreadySet = errors.New("already set")

// mergeNodeExtraCACerts adds the CA path to the env object of a Claude Code
// settings document, preserving everything else in the file.
//
// It returns the document to write and whether anything changed. An existing
// identical value changes nothing. An existing different value returns
// errCATrustAlreadySet, and a document that is not the shape a settings file
// has returns an error rather than a replacement: a file that will not parse
// is someone's configuration with a typo in it, and overwriting it would
// destroy every other setting they have.
func mergeNodeExtraCACerts(existing []byte, caPath string) ([]byte, bool, error) {
	document := map[string]any{}
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &document); err != nil {
			return nil, false, errors.New("not valid JSON")
		}
	}

	env := map[string]any{}
	if held, ok := document["env"]; ok {
		env, ok = held.(map[string]any)
		if !ok {
			return nil, false, errors.New(`"env" is not an object`)
		}
	}

	if held, ok := env[nodeExtraCACerts]; ok {
		if current, isString := held.(string); !isString || current != caPath {
			return nil, false, fmt.Errorf("%s %w: %v", nodeExtraCACerts, errCATrustAlreadySet, held)
		}
		return nil, false, nil
	}

	env[nodeExtraCACerts] = caPath
	document["env"] = env

	body, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return append(body, '\n'), true, nil
}

// claudeSettingsPath is where Claude Code keeps the settings it applies to
// every project, which is the scope the MCP entry was registered at.
func claudeSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// trustCAInClaudeCode names the CA in Claude Code's settings file and reports
// what to show in the results table.
func trustCAInClaudeCode(caPath string) (string, error) {
	path, err := claudeSettingsPath()
	if err != nil {
		return "", err
	}
	// #nosec G304 -- the path is this user's own home directory joined with a fixed name.
	existing, readErr := os.ReadFile(path)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return "", readErr
	}

	body, changed, err := mergeNodeExtraCACerts(existing, caPath)
	switch {
	case errors.Is(err, errCATrustAlreadySet):
		return "", err
	case err != nil:
		return "", fmt.Errorf("%s: %w", displayPath(path), err)
	case !changed:
		return "already trusted", nil
	}

	perm := os.FileMode(0600)
	if info, statErr := os.Stat(path); statErr == nil {
		perm = info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	if err := atomicWriteFile(path, body, perm); err != nil {
		return "", err
	}
	return displayPath(path), nil
}

// caTrustOutcome is one client's answer to "can it verify this server".
type caTrustOutcome struct {
	detail string
	status string
	// note is the instruction printed under the table when the installer
	// could not arrange the trust itself. Silence here is what produces a
	// configured client that fails its first request for a reason nobody
	// named.
	note string
}

// clientCATrust arranges the CA trust each configured client needs.
//
// Only Claude Code has a place in its own configuration to put it. Codex reads
// no certificate from ~/.codex/config.toml, and Cursor's mcp.json has no such
// field either, so for those two this returns the one line that says what to
// do by hand.
func clientCATrust(clientID, caPath string) caTrustOutcome {
	switch clientID {
	case "claude":
		detail, err := trustCAInClaudeCode(caPath)
		if err != nil {
			return caTrustOutcome{
				detail: err.Error(),
				status: "⚠ manual",
				note: fmt.Sprintf("Claude Code: set %s=%s in the env object of ~/.claude/settings.json.",
					nodeExtraCACerts, displayPath(caPath)),
			}
		}
		return caTrustOutcome{detail: detail, status: "✓ trusted"}
	case "cursor":
		return caTrustOutcome{
			detail: "no CA field in mcp.json",
			status: "⚠ manual",
			note: fmt.Sprintf("Cursor runs on Node: launch it from a shell that has sourced the env\n  file above, which exports %s=%s.",
				nodeExtraCACerts, displayPath(caPath)),
		}
	case "codex":
		return caTrustOutcome{
			detail: "no CA field in config.toml",
			status: "⚠ manual",
			note: fmt.Sprintf("Codex reads no certificate from its config. If it cannot verify the\n  server, add %s to the trust store its TLS stack reads.",
				displayPath(caPath)),
		}
	default:
		return caTrustOutcome{}
	}
}
