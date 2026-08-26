package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The MCP clients this installer can configure. Three, deliberately: each one
// needs its own file format and its own answer to "where does the token come
// from", so every addition is real work rather than another name in a list.
//
// A client that is not installed is never offered. Suggesting Codex to someone
// who does not have it is noise, and worse, it invites a yes that then writes a
// config file for a tool that will never read it.
const (
	mcpTokenEnvVar = "OBERTH_TOKEN"
	mcpServerName  = "oberth"
)

// mcpClient is one target the offer can configure.
type mcpClient struct {
	id    string
	label string
	// configure applies the server to this client and returns what to show in
	// the results table. An error here is reported and never fails an install.
	configure func(ctx context.Context, deps Deps, baseURL, tokenCommand string, config []byte) (string, error)
}

// detectMCPClients returns only the clients actually present on this machine,
// in a stable order.
func detectMCPClients(deps Deps) []mcpClient {
	lookPath := deps.LookPath
	if lookPath == nil {
		return nil
	}
	var found []mcpClient
	if _, err := lookPath("claude"); err == nil {
		found = append(found, mcpClient{
			id:    "claude",
			label: "Claude Code",
			configure: func(ctx context.Context, deps Deps, _, _ string, config []byte) (string, error) {
				if registerWithClaudeCode(ctx, deps, config) {
					return "registered", nil
				}
				return "", errors.New("claude mcp add-json failed")
			},
		})
	}
	if _, err := lookPath("codex"); err == nil {
		found = append(found, mcpClient{
			id:    "codex",
			label: "Codex",
			configure: func(_ context.Context, _ Deps, baseURL, _ string, _ []byte) (string, error) {
				return configureCodex(baseURL)
			},
		})
	}
	// Cursor is a GUI application, so the binary is not the reliable signal:
	// the `cursor` shell command is an optional install step many people skip.
	// The config directory is what Cursor itself creates.
	if _, err := lookPath("cursor"); err == nil {
		found = append(found, cursorClient())
	} else if home, homeErr := os.UserHomeDir(); homeErr == nil {
		if info, statErr := os.Stat(filepath.Join(home, ".cursor")); statErr == nil && info.IsDir() {
			found = append(found, cursorClient())
		}
	}
	return found
}

func cursorClient() mcpClient {
	return mcpClient{
		id:    "cursor",
		label: "Cursor",
		configure: func(_ context.Context, _ Deps, baseURL, _ string, _ []byte) (string, error) {
			return configureCursor(baseURL)
		},
	}
}

// chooseMCPClients asks which of the detected clients to configure. With a
// single client there is no list worth showing, so it asks about that one
// directly.
func chooseMCPClients(ctx context.Context, deps Deps, color bool, detected []mcpClient, ask bool) ([]mcpClient, error) {
	if len(detected) == 0 {
		return nil, nil
	}
	// Nothing to ask on, or nobody to ask: configure everything found. That is
	// the answer the prompt defaults to, and the one a scripted install wants.
	if !ask || deps.IsTerminal == nil || !deps.IsTerminal() || deps.Input == nil {
		return detected, nil
	}
	if len(detected) == 1 {
		options := []string{detected[0].label, "Write the file only"}
		choice, err := selectOption(ctx, deps, color, "MCP client", options, 0)
		if err != nil {
			return nil, err
		}
		if choice == 1 {
			return nil, nil
		}
		return detected, nil
	}

	labels := make([]string, 0, len(detected))
	for _, client := range detected {
		labels = append(labels, client.label)
	}
	options := append([]string{"All (" + strings.Join(labels, ", ") + ")"}, labels...)
	options = append(options, "Write the file only")

	choice, err := selectOption(ctx, deps, color, "MCP clients", options, 0)
	if err != nil {
		return nil, err
	}
	switch {
	case choice <= 0:
		return detected, nil
	case choice <= len(detected):
		return detected[choice-1 : choice], nil
	default:
		return nil, nil
	}
}

// configureCodex writes the server into ~/.codex/config.toml.
//
// Codex has no `mcp add` for HTTP servers, only for stdio ones, so the file is
// the interface. env_http_headers names an environment variable rather than
// carrying a token, which keeps the credential out of the file the same way
// Claude Code's headersHelper does, at the cost of needing the variable
// exported before Codex starts.
func configureCodex(baseURL string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, ".codex", "config.toml")
	// #nosec G304 -- the path is this user's own home directory joined with a fixed name.
	existing, readErr := os.ReadFile(path)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return "", readErr
	}
	// Editing TOML in place would need a parser this package does not depend
	// on. Appending is safe when the table is absent; when it is present the
	// existing entry is left alone and said so, because silently rewriting
	// someone's configuration is worse than telling them it is already there.
	if strings.Contains(string(existing), "[mcp_servers."+mcpServerName+"]") {
		return "already configured", nil
	}
	block := fmt.Sprintf("\n[mcp_servers.%s]\nurl = %q\nenv_http_headers = { Authorization = %q }\n",
		mcpServerName, baseURL+"/mcp", mcpTokenEnvVar)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	if err := atomicWriteFile(path, append(existing, []byte(block)...), 0600); err != nil {
		return "", err
	}
	return displayPath(path), nil
}

// configureCursor merges the server into ~/.cursor/mcp.json.
//
// Cursor resolves ${env:VAR} in headers when it loads the file, so the token
// stays out of the file here too. It resolves at load time rather than per
// request, which means Cursor has to be started from an environment that
// already carries the variable.
func configureCursor(baseURL string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, ".cursor", "mcp.json")

	document := map[string]any{}
	// #nosec G304 -- the path is this user's own home directory joined with a fixed name.
	existing, readErr := os.ReadFile(path)
	switch {
	case readErr == nil:
		// A file that is present but unparseable is someone's configuration
		// with a typo in it. Replacing it would destroy the rest of their
		// servers, so this stops instead.
		if err := json.Unmarshal(existing, &document); err != nil {
			return "", fmt.Errorf("%s is not valid JSON", displayPath(path))
		}
	case !errors.Is(readErr, os.ErrNotExist):
		return "", readErr
	}

	servers, _ := document["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	servers[mcpServerName] = map[string]any{
		"url": baseURL + "/mcp",
		"headers": map[string]any{
			"Authorization": "Bearer ${env:" + mcpTokenEnvVar + "}",
		},
	}
	document["mcpServers"] = servers

	body, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	if err := atomicWriteFile(path, append(body, '\n'), 0600); err != nil {
		return "", err
	}
	return displayPath(path), nil
}
