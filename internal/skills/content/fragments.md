---
name: oberth-fragments
description: Share a pipeline step between repositories with Oberth fragments. Use when the same steps are copied across repos, when a templateRef is refused, or when asked how to publish or pin a shared pipeline.
---

# Cross-repo pipeline fragments

A fragment is an ordinary Oberth repository holding a Workflow document at
`.oberth/fragment.yaml`. Other repositories reference it with Argo's own
syntax, pinned to a tag.

## Consuming one

```yaml
steps:
  - - name: verify
      templateRef:
        name: transferz/maven-verify@v3
        template: verify
```

The repository half is the same path used to push, `<upstream>/<repository>`.
A reference with no `@version` is refused: a fragment is always pinned.

## What the server does with it

Before admission the server resolves the tag to a commit, reads
`.oberth/fragment.yaml` at that commit, inlines all of its templates under
generated `frag-<hash>-<template>` names, rewrites references between them, and
deletes the `templateRef`. The gate then reads one flat document containing
everything that will run.

A `templateRef` the gate still sees is refused, so a resolution failure fails
the run rather than submitting something unreviewed.

## Publishing a version

Not simply `git tag && git push`. Three things must hold:

1. The commit must be reachable from the upstream default branch, because the
   release-admission ref tracks the upstream mirror rather than Oberth's copy.
2. The fragment repository needs its own `.oberth/build.yaml` and a green run.
3. Release tags should be annotated.

The practical consequence is that a fragment is verified by its own CI before
anyone can pin it.

## What a fragment cannot do

Fragments do not nest: one containing its own `templateRef` is refused.

A fragment cannot declare secret paths, only spend from what the consuming
repository already declared and was granted. Its own
`oberth secretstore exec --path` invocations are checked against the consumer's
approval table.

Everything the admission gate refuses in a pipeline it also refuses inside a
fragment. Being a fragment grants nothing.

## Inspecting

`oberth fragments list` shows which repositories publish fragments and at which
versions. `oberth fragments show <repo>@<tag>` prints the document and the
commit the tag resolved to.
