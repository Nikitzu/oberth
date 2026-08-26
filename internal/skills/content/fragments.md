---
name: oberth-fragments
description: Share a pipeline step or a data file between repositories with Oberth fragments and file dependencies. Use when the same steps are copied across repos, when a templateRef is refused, when a step needs a file from another repository, or when asked how to publish or pin shared pipeline content.
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
        name: acme/maven-verify@v3
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

## Sharing a file rather than a step

A fragment shares templates. When a step needs shared *data* -- a registry, a
policy list, an identifier map -- declare a file dependency instead:

```yaml
metadata:
  annotations:
    oberth.ci/files: |
      depgraph@v1:graph/repos.yml
      policy@v3:ci/allowed-images.txt
```

Read it under `$OBERTH_FILES`, which is `/work/files`. The layout under it is
the repository name then the file's own path, so the first of those lands at
depgraph/graph/repos.yml inside that directory.

The same pinning rule applies: `repository@version:path`, always a tag. The
server resolves and reads it, so the pipeline needs no credential -- which is
the point. Cloning another repository from a pipeline would need an uplink key,
and an uplink key can promote to main.

The mount is read-only and sits outside `/work/src`, so a repository cannot
shadow a delivered file with one of its own. On the submitted Workflow the
annotation holds the resolved lock -- commit and content digest per file -- not
what was written, so what a run read is checkable after the fact.

Limits: 8 files, 1 MiB each, 4 MiB in total. Over any of them the run is
refused rather than the file truncated.

## Inspecting

`oberth fragments list` shows which repositories publish fragments and at which
versions. `oberth fragments show <repo>@<tag>` prints the document and the
commit the tag resolved to. `oberth files show <repo>@<tag>:<path>` does the
same for one file dependency.
