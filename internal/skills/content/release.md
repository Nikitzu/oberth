---
name: oberth-release
description: Work with Oberth's credentialed release tier and secret store paths. Use when a step needs a secret, when a release pipeline fails at login, or when asked how Oberth grants credentials.
---

# The credentialed tier

A pipeline is credentialed when it declares secret-store paths it will read,
and not otherwise. The trigger is not the deciding factor: a release with no
declared paths runs with no token and no vault wiring, exactly like a branch
build.

## Declaring and using a path

Declare on the workflow:

```yaml
metadata:
  annotations:
    oberth.ci/secret-paths: oberth/upstream/<org>/<repo>/<secret>
```

Read it in a step by making the container's command the Oberth binary:

```yaml
command: ["/run/oberth/bin/oberth"]
args: ["secretstore", "exec", "--dir", "/run/oberth-secrets",
       "--path", "oberth/upstream/<org>/<repo>/<secret>", "--", "sh", "-euc", "..."]
```

Wrapping the chain in a shell instead leaves the step with no token. The
projected token and the secrets volume are mounted only onto templates whose
command is the Oberth binary followed by `secretstore exec`.

Values arrive as files under `$OBERTH_SECRETSTORE_DIR`, one directory per
secret.

## Two gates, not one

Declaring a path is not the same as being allowed to read it. Every declared
path must also carry an active approval-table grant, made by an administrator
with `oberth access allow`. Admission refuses a `--path` that is not in the
workflow's own annotation, and refuses a declared path with no grant.

## What redaction covers

Secrets are redacted write-side, by wrapping the step's stdout and stderr. A
file written by the step never passes through that writer, so a secret copied
into `$OBERTH_ARTIFACTS` is stored unredacted. Do not write secrets to files
you expect to be kept.

## When a release fails at login

An `x509: certificate signed by unknown authority` at the first credentialed
step usually means the deployment has no trust anchor configured for a
cluster-internal store address. That is an operator setting, not a pipeline
one.
