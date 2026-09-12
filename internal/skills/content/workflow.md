---
name: oberth-workflow
description: Read this first. Which Oberth verb for which moment, from onboarding a repository to a green run, on either engine. Use before touching a repository's CI, before opening a PR in an onboarded repository, or when unsure what Oberth expects next.
---

# Working with Oberth

Oberth runs a repository's pipeline before a commit reaches the forge. The
evidence that tests pass is a green run for the exact HEAD commit; a local
test run is not evidence, and there is no override. Every verb below has an
MCP tool of the same name where this session has the MCP server; the `oberth`
command works everywhere.

## The verbs, by moment

```
oberth onboard                      # repository not on the server yet: one line, from its checkout
git push oberth HEAD:refs/heads/<b> # start a run for HEAD (onboard did this once already)
oberth wait <sha|run-id>            # block until the run is terminal; exits non-zero on red
oberth run <id>                     # which burn and step failed
oberth log <id> --burn <b> --step <s> -pattern <re> -context 3
oberth repo pipeline check <repo>   # has the repository's own CI drifted from the stored pipeline
oberth repo pipeline show|set <repo> [file]
oberth validate [--engine=docker]   # admit a pipeline document without pushing
oberth secretstore put --engine=docker <path> field=value   # a credential a pipeline declares (docker engine)
oberth unseal                       # the store came back sealed after a restart
oberth status                       # the deployment: engine, store, upstreams, drift warnings
```

`onboard` registers the repository, generates a pipeline from the repository's
own workflows and manifests, validates it through admission, stores it on the
server, adds the `oberth` git remote with the right push identity, pushes HEAD
and waits. Re-running it changes nothing that is already in place. On a red
run it says in one sentence whether the pipeline or the repository is at
fault; it repairs its own generator mistakes, never the repository's checks.

Pipelines are server-held: `.oberth/build.yaml` never has to be committed, and
a stored document wins only when the commit carries none.

## Watching a run

Use `oberth wait`, or the MCP `wait` tool. Never hand-roll a polling loop over
`oberth runs`; every one written so far has had a matching bug and reported
late. On red, `oberth run <id>` names the burn and step, then read that step's
log with a pattern. The oberth-triage skill covers the filters.

## The two engines

The same binary serves both; the pipeline document is the same on both.

- Cluster (Argo Workflows on Kubernetes): installed with `oberth install`,
  reached on the ports the install printed, secrets in an in-cluster OpenBao
  the installer seeds from the forge token.
- Local (Docker engine): installed with `oberth install --engine=docker`,
  reached on https://localhost:8443 and git on 127.0.0.1:8022 by default,
  material under ~/.oberth/local, secrets in an OpenBao container written with
  `oberth secretstore put --engine=docker`. Steps run sequentially, and a
  document using a construct the engine refuses is rejected at submission;
  `oberth validate --engine=docker` finds that before a push.

`oberth status` says which engine answers. The client environment file the
install wrote (`~/.config/oberth/env`) selects the server; source it.

## The completion gate

Pushing to the forge and opening or merging a PR in an onboarded repository
should wait for a green run for the exact HEAD. A red or missing run is never
a reason to go around it. If a person decides to skip Oberth for one push,
that is their call to state, and the result is reported as untested.

## When something is wrong

- "sealed" or connection refused from the store: `oberth unseal`.
- A pipeline declares a secret path the store lacks: `onboard` prints the
  exact path and the `secretstore put` line for it.
- A drift warning in `oberth status`: the repository's own CI inputs changed
  since the pipeline was stored; `oberth repo pipeline check <repo>` shows the
  diff and `--store` adopts it.
- A construct refused at submission: the oberth-pipeline skill.
- A step needing a credential or the release tier: the oberth-release skill.
