# MCP setup

Connect an AI agent (Claude Code or any MCP client) to Oberth.

## Prerequisites

- Oberth is deployed and has at least one upstream registered
- `kubectl` access to the Oberth namespace

## 1. Create an uplink

Run inside the Oberth pod:

```bash
kubectl exec -i -n oberth deploy/oberth -- \
  oberth uplink add - operator@host < ~/.ssh/id_ed25519.pub
```

Replace `operator@host` with a descriptive identity for this uplink.

The command prints three lines exactly once. Save both values:

```
TLS certificate fingerprint: SHA256:abc123...
Uplink token for operator@host (shown once):
oberth_xxxxxxxxxxxxxxxx
```

The token is never stored or recoverable. If lost, create a new uplink.

## 2. Configure Claude Code

Add to `.claude/settings.local.json` (user-scoped, never committed) or user-level
config. **Do not** place bearer tokens in `.claude/settings.json` — that file
is typically checked into the repository.

### Via the HTTPS NodePort

```json
{
  "mcpServers": {
    "oberth": {
      "type": "url",
      "url": "https://<node-address>:30443/mcp",
      "headers": {
        "Authorization": "Bearer oberth_xxxxxxxxxxxxxxxx"
      }
    }
  }
}
```

This is the shipped access path — Oberth exposes fixed NodePorts and carries
no tunnel subsystem. If you front the NodePort with your own TLS-terminating
proxy (an ingress, a Cloudflare Tunnel you operate), point the URL at that
hostname instead; a publicly trusted certificate there removes the
client-side trust step below.

Direct access uses a self-signed certificate. The client must trust it.
Export the certificate and add it to the system trust store:

```bash
kubectl get secret -n oberth oberth-tls \
  -o jsonpath='{.data.tls\.crt}' | base64 --decode > oberth-tls.crt

# Verify fingerprint matches uplink add output
openssl x509 -in oberth-tls.crt -outform DER | \
  openssl dgst -sha256 -binary | openssl base64 -A | \
  sed 's/^/SHA256:/' | tr -d '='

# Linux (Debian/Ubuntu)
sudo cp oberth-tls.crt /usr/local/share/ca-certificates/oberth.crt
sudo update-ca-certificates

# macOS
sudo security add-trusted-cert -d -r trustRoot \
  -k /Library/Keychains/System.keychain oberth-tls.crt
```

## 3. Verify the connection

After restarting Claude Code, the MCP server appears in the tool list. Verify
by asking Claude Code to call any Oberth tool, or test manually:

```bash
curl --fail-with-body --silent \
  --cacert oberth-tls.crt \
  --resolve "oberth:30443:<node-address>" \
  -H "Authorization: Bearer oberth_xxxxxxxxxxxxxxxx" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' \
  "https://oberth:30443/mcp"
```

A successful response returns 21 tools. Behind an operator-run TLS-terminating
proxy with a publicly trusted certificate, drop `--cacert` and `--resolve` and
call the proxy hostname directly.

## MCP tools

| Tool | Description |
|------|-------------|
| `status` | CI status for a SHA or branch, including the failed step |
| `logs` | One named step's log output for a SHA, optionally filtered |
| `run_get` | One exact durable run and its named step results by run ID |
| `run_logs` | One exact burn/step log by durable run ID, optionally filtered |
| `wait` | Long-poll until a SHA reaches a terminal state |
| `sync` | Park a WIP branch upstream without a green gate (not completion evidence) |
| `promote` | Green-gate a SHA, merge with target branch, push without force |
| `promote_status` | Wait for a promotion record to become terminal |
| `issue_create` | Create a workspace-global manual issue |
| `issue_get` | Get an issue by ID |
| `issue_update` | Update an issue title and body |
| `issue_close` | Close an issue |
| `issue_delete` | Delete an accidentally created manual issue |
| `issue_list` | List issue IDs and states (paginated, 50 per page) |
| `issue_lock` | Acquire or renew a five-minute caller-owned issue lock |
| `access_list` | List secret access grants for a repository |
| `access_allow` | Grant a step access to a secret path (admin-only) |
| `access_revoke` | Revoke a step's access to a secret path (admin-only) |

### Filtering log output

`logs` and `run_logs` accept five optional parameters. Filtering happens on the
server, so a narrowed read never sends the whole step over the wire.

| Parameter | Meaning |
|------|-------------|
| `pattern` | RE2 expression; only matching lines are returned |
| `context` | Lines of context on each side of a match (max 50) |
| `offset` | First line to return, 0-based. Pages over matches when `pattern` is set, over raw lines otherwise |
| `limit` | Maximum lines returned. Omit for no line cap |
| `tail` | Take from the end of the step rather than the start |

`pattern` matches against the line with its `[burn/step]` prefix removed, so `^`
and `$` anchor to the output a step actually produced. The returned bytes keep
the prefix.

Every response carries counts so a narrowed read is never mistaken for a
complete one:

| Field | Meaning |
|------|-------------|
| `total_lines` | Lines in the step before any filtering |
| `matched_lines` | Lines matching `pattern`; equals `total_lines` without one |
| `returned_lines` | Lines actually in this response |
| `truncated` | Whether a limit or the response ceiling withheld anything |
| `bytes` | True size of the full step range |
| `line_numbers` | Original position of each returned line |

A step above the 4 MiB response ceiling returns a truncated result with
`truncated: true` rather than failing. Prefer a pattern to retrieving a whole
step: a single step can exceed a model's context window.

| `repo_list` | List registered repositories with upstream and probe state |
| `repo_remove` | Remove a repository mapping and its Git cache (admin-only) |
| `run_list` | List recent runs with optional repo/ref filter (bounded page) |
| `system_status` | System health: database, upstreams, cluster, audit, version |

## JSON API

Authenticated `GET` endpoints serve the same state as a web dashboard:

| Endpoint | Content |
|----------|---------|
| `/api/runs` | All CI runs |
| `/api/repos` | Registered repositories |
| `/api/issues` | All issues |
| `/api/status` | System health summary |

Unauthenticated: `/healthz` (liveness) and `/readyz` (dependency readiness).

## Notes

- The MCP endpoint accepts `POST /mcp` with JSON-RPC 2.0 (protocol version
  `2025-03-26`).
- Bearer tokens are bound 1:1 to an uplink SSH public-key fingerprint and
  identity.
- Run selectors (`ref`, `sha`) are resolved across all repositories; pass
  `repo` only when a short SHA or branch name is ambiguous.
- `sync` parks a branch upstream for visibility. It is not integration or
  completion evidence.
- `promote` requires explicit integration authority, an exact green SHA, and a
  named target branch. It never force-pushes.
