# Secrets on the docker engine

`oberth serve --engine=docker` runs pipeline steps as plain containers on a
local Docker daemon. There is no Kubernetes, so there is no kubelet to mint the
ServiceAccount token a credentialed step logs in to OpenBao with, and no
`kubernetes` auth method to bind a role to a ServiceAccount name.

The answer is to keep OpenBao and change the auth method, not the store. The
server signs a short-lived JWT per run whose subject is the run's tier, and
OpenBao's `jwt` auth method binds a role to that subject exactly as a
`kubernetes` role binds a ServiceAccount name.

## Setup

```
oberth secretstore init --engine=docker
```

One command, idempotent, safe to re-run. It:

1. starts OpenBao in a container with file storage on a named volume, published
   on the loopback only, from a digest-pinned image;
2. initialises it with one key share and stores the unseal key and the root
   token in the macOS keychain under `oberth-openbao-unseal` and
   `oberth-openbao-root`, then unseals it;
3. generates the run-identity signing key at `~/.oberth/jwt-signing.pem`, mode
   0600, if it does not exist;
4. enables the `jwt` auth mount and configures it with the public half of that
   key and a bound issuer;
5. enables the `oberth` KV v2 mount;
6. writes the `oberth-ci` and `oberth-release` policies and the two `jwt` roles
   that carry them, each bound to its own subject and its own audience.

Then start the server:

```
oberth serve --engine=docker ... \
  --secretstore-address=http://127.0.0.1:8200 \
  --secretstore-jwt-signing-key=$HOME/.oberth/jwt-signing.pem
```

`--secretstore-jwt-signing-key` is the only flag this adds. Everything else the
credential chain needs was already a serve flag, and the role names are the
ones `secretstore init` created.

A laptop reboot leaves the store sealed, which presents to a pipeline as a
connection failure rather than as anything mentioning a seal. `oberth
secretstore unseal --engine=docker` is the repair, and it reads the key from
the keychain rather than asking for it.

## The tier boundary

Two policies, and they are the whole boundary:

```
# oberth-ci
path "oberth/data/upstream/*" { capabilities = ["read"] }

# oberth-release
path "oberth/data/*"          { capabilities = ["read"] }
```

A CI run's identity carries the subject `oberth-ci`, whose role carries the
first policy. Asking for `oberth/data/release/signing` gets HTTP 403 from
OpenBao. The refusal is the store's, not Oberth's, which is the property the
in-cluster deployment has and the one this design exists to keep.

`internal/secretstore/jwtauth_live_test.go` asserts it against a live store,
along with the fact that a CI subject presented to the release role cannot log
in at all. Run it with:

```
OBERTH_BAO_PROBE_ADDR=http://127.0.0.1:8200 \
OBERTH_BAO_PROBE_KEY=$HOME/.oberth/jwt-signing.pem \
  go test ./internal/secretstore/ -run TestLiveJWT
```

## What a run sees

Identical to the Argo path, deliberately, so a repository cannot tell which
engine ran it:

* `/run/oberth-secrets`, a private tmpfs, is where `oberth secretstore exec`
  materialises the declared secrets. The command is not modified: it still
  refuses to write credentials to a filesystem that is not memory backed.
* `VAULT_ADDR`, `OBERTH_VAULT_ROLE` and `OBERTH_SECRETSTORE_KV_MOUNT` are
  injected on credentialed runs only.
* The minted identity is delivered read-only on a per-run volume at
  `/var/run/secrets/kubernetes.io/serviceaccount/token`, the path the projected
  token occupies in a cluster. The path name is a Kubernetes convention rather
  than a Kubernetes dependency, and using it is what lets the client and
  `secretstore exec` run byte for byte unchanged.
* An uncredentialed run gets none of it, not even the address.

Declared paths are authorized before anything runs, by the same rules the Argo
path applies: an upstream-scoped path is checked structurally against the
declaring repository's own org and name, and a system-namespace path is refused
outright on the CI trigger.

## What genuinely degrades

Three things, and they are narrower than one might expect, but they are real.

**The server holds the signing key.** In a cluster the kubelet mints the token
and Oberth cannot fabricate one; here a compromised server process can mint an
`oberth-release` subject during a CI run. Note what this is not: it is not
"Oberth decides who may read a release credential". OpenBao still enforces the
policy for whoever it believes is asking. What is weaker is who it can be
persuaded to believe. Narrowing this further means a second signing key for the
release tier, readable only after an interactive keychain prompt, which turns a
release into an attended operation. On one laptop that may be the right trade;
it should be an explicit choice rather than a default.

**No TokenReview, so no revocation before expiry.** Kubernetes auth validates
the token against the API server, so deleting the ServiceAccount kills it
immediately. A signed JWT is good until `exp`. The TTL is ten minutes, and the
in-cluster projected token is already 600 seconds, so this is close to parity.

**Same-host blast radius.** OpenBao listens on the loopback of the machine
running the pipeline containers, and step containers reach it through the
daemon's host gateway. Anything that can reach the host network from inside a
step can reach the store's port. It cannot read anything without a valid
identity, but the port is reachable in a way an in-cluster NetworkPolicy would
have covered.

## Per-repository identities

`secretstore.RunSubject` takes the org and the repository already and returns
only the tier. Upstream v0.13.31 gives a grant-holding repository its own
ServiceAccount and its own Vault role scoped to
`<kv>/data/upstream/<org>/<repo>/*`. The same move here is to return
`<tier>-<org>-<repo>` and have `secretstore init` write one `jwt` role per
repository bound to that subject; the minting call site already knows the org
and the repo, the policy is generated the same way, and the client is
untouched. What is missing is the moment to create the role, because the docker
engine has no grant reconciler. That is the work, and it is recorded at the
definition of `RunSubject` so it is found from the code rather than from here.
