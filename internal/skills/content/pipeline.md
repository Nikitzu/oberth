---
name: oberth-pipeline
description: Author or fix an Oberth pipeline in .oberth/build.yaml. Use when adding CI steps, when a push is refused by admission, or when asked why Oberth rejected a workflow.
---

# Authoring an Oberth pipeline

A pipeline is a real Argo Workflow document at `.oberth/build.yaml` for a
branch push, or `.oberth/release.yaml` for a tag. It is decoded strictly: an
unknown field is an error, not an ignored line.

## The gate refuses things, and each refusal has a reason

The server reads one flat document and rejects every construct through which a
repository could reach substrate it was not granted. The common ones:

- `artifacts` and `artifactRepositoryRef` resolve credentials from a Kubernetes
  Secret. Write to `$OBERTH_ARTIFACTS` instead and the server collects it.
- `templateRef` points outside the document. Use a pinned cross-repo fragment,
  which the server resolves and inlines before admission.
- `podSpecPatch`, `hostPath`, `hostNetwork`, `nodeSelector`, `tolerations`,
  `securityContext` and Secret or ConfigMap environment references are refused
  outright. The server forces the container baseline itself.
- `resource` and `plugin` templates are refused.

Do not work around a refusal. Each one names a way to reach a credential, a
host, or another tenant's identity.

## What the server provides

- The checkout, read-only, at `/work/src`
- `$OBERTH_ARTIFACTS`, writable, collected when the run ends
- `$OBERTH_REPO`, `$OBERTH_REF`, `$OBERTH_SHA`, `$OBERTH_TRIGGER`,
  `$OBERTH_RUN_ID`
- A node-local build cache, split by trust tier

## Declaring size

`oberth.ci/size` is `S`, `M`, `L` or `XL` and decides the run's scheduling
weight. An undeclared step is `M`.

## Ordering steps

Put the cheapest gate first. The DAG stops at the first failure, so a lint
failure at 42 seconds is worth more than a complete verdict at seven minutes.

## Before pushing

`oberth validate` decodes the document, runs the same admission the server
runs, and prints the step inventory a push would produce.

## When the document does not live in the repository

A repository can keep its pipeline on the server instead of committing it, so
`.oberth/build.yaml` never appears in a pull request. The stored bytes meet the
same decode and the same admission; nothing about a run changes.

- `oberth repo pipeline show <repo>` prints what the server holds
- `oberth repo pipeline set <repo> <file>` stores a new version, refusing on
  the same admission message a push would produce
- `oberth repo pipeline unset <repo>` withdraws it; earlier versions stay
  readable and pushes must carry the file again

A commit that carries the file always wins over the stored document. Alongside
the document the server keeps a fingerprint of the files `oberth init` reads,
and a run whose repository has since changed one of them is flagged as drifted.
The run still ran. `oberth repo pipeline check <repo>` regenerates the pipeline
from a commit, prints the diff against the stored one, and exits non-zero when
they differ.
