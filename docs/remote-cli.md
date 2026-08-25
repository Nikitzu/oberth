# Reading Oberth from a terminal

Every other `oberth` command opens the server's database and git cache directly,
so it only works inside the pod. These read the HTTP API, so they work from the
machine you pushed from.

## Configuration

| Variable | Meaning |
|---|---|
| `OBERTH_BASE_URL` | The server's address. Setting it is what makes a command read the server |
| `OBERTH_TOKEN` | An uplink bearer token |
| `OBERTH_TOKEN_COMMAND` | A command whose standard output is the token, used when `OBERTH_TOKEN` is unset |
| `OBERTH_CA_CERT` | PEM of the authority that signed the server's certificate |

Oberth never writes a token to disk. There is no config file and no
`oberth login`. `OBERTH_TOKEN_COMMAND` exists so the token can come from a
password manager or the system keychain without ever landing in a file:

```
export OBERTH_TOKEN_COMMAND='op read "op://vault/oberth/credential"'
```

## Commands

```
oberth runs [--repo NAME] [--ref REF] [--limit N]
oberth run <run-id>
oberth log <run-id> --burn B --step S [--pattern P] [--context N] [--offset N] [--limit N] [--tail]
oberth artifacts <run-id> [name]
oberth repos
oberth issues
oberth status
```

All read-only. Publishing and cancelling stay in the dashboard and MCP.

`--json` on any of them emits the server's own payload unchanged, so a script
never parses a rendered table.

`oberth log` accepts the same five filters as the `logs` MCP tool and prints the
same counts, on stderr. A step log can exceed a context window, so prefer a
pattern over retrieving the whole thing:

```
oberth log run-abc --burn ci --step test --pattern 'FAIL|Error' --context 3
```

The `[burn/step] ` prefix is stripped from each line, since the step is already
named in the command. `--raw` keeps it.

## Local or remote

`artifacts` reads either. **If `OBERTH_BASE_URL` is set it reads the server;
otherwise it reads the local store**, which is the only thing that works inside
the pod. Every command that can go either way prints which it used:

```
reading: server
reading: local store
```

That line goes to stderr, so it never contaminates output you pipe or redirect.

## Certificates

TLS verification is always on and there is no flag to disable it. A CI client
that can be told to trust anything is a CI client that will be.

Two failures look similar and need different fixes, so the client names which
one happened.

**Unknown authority** means the signer is not in this machine's trust store.
Point `OBERTH_CA_CERT` at the PEM that signed the server's certificate.

**A hostname mismatch** means the certificate does not cover the address you
used, and no trust anchor can fix that. The client lists the names the
certificate does carry:

```
client: the server's certificate does not cover "localhost"; it is issued for
oberth, oberth.oberth, oberth.oberth.svc, oberth.oberth.svc.cluster.local
```

The default chart issues a certificate for in-cluster names only, so a developer
reaching the server on any other address will hit this. Either reach it by a
name the certificate carries, or have whoever runs the server add the address
they expect people to use. This is a deployment decision, not something the
client should route around.
