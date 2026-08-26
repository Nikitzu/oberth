---
name: oberth-triage
description: Read a failed Oberth CI run without spending a context window. Use when a run is red, when asked why CI failed, or before fetching any run log.
---

# Triaging a red Oberth run

A whole run log is 400,000 to 600,000 tokens, past most context windows. A
single step is 0.6% to 3.4% of it. Never fetch a run log whole.

Two surfaces reach the same server and take the same filters: the MCP tools,
and the `oberth` command. Use whichever this session has.

```
oberth status                       # the deployment
oberth runs                         # recent runs
oberth run <id>                     # which burn and step failed
oberth log <id> -pattern <re> -context 3
oberth artifacts <id>
```

Every command takes -json to emit the server's payload unchanged.

## Order of operations

1. `run_get`, or `oberth run`, tells you which burn and step failed. Start
   there, not with the log. `status` covers the deployment.
2. `run_logs`, or `oberth log`, with a `pattern` for the failure, not without
   one. Add `context` of 3 to see the lines around each match.
3. Read `matched_lines` and `returned_lines` in the response. If they differ,
   you are seeing a subset and the counts say by how much. The command prints
   the same counts.
4. If the run kept files, `artifacts` lists them and `artifact_get` reads one,
   with the same filters. A test report usually explains a failure that the log
   only reports.

## The filter parameters

`logs`, `run_logs` and `artifact_get` accept the same five, and the command
takes each as a flag of the same name:

- `pattern`, an RE2 regular expression, at most 512 bytes
- `context`, lines each side of a match, at most 50
- `offset` and `limit`, which page over **matches** when a pattern is set and
  over raw lines when it is not
- `tail`, to read from the end

The command reads its endpoint and credential from the environment. If it
reports no server to talk to, the install wrote a file to source:

```
. ~/.config/oberth/env
```

## Reading the counts

Every response reports `total_lines`, `matched_lines`, `returned_lines`,
`truncated` and `bytes`. A narrowed read that cannot be told apart from a
complete one is worse than too much output, so the counts are the point. If
`truncated` is true you did not see everything, whatever the body suggests.

## What not to conclude

A step named `frag-<hash>-<template>` came from another repository's pinned
pipeline fragment, not from the repository under test. A failure there is
usually the pinned version rather than this commit.

A run whose trigger is `schedule` was not caused by anyone's push. The commit
may be days old and still green in every other sense; look for something that
changed outside the repository.
