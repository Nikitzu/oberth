# Agent skills

Oberth ships the knowledge an agent needs to use it, in the binary, versioned
with the server.

An agent meeting Oberth for the first time fetches whole run logs, does not know
the admission gate refuses `templateRef` or why, does not know a failed run kept
a test report, and rediscovers all of it by trial and error. That is expensive
in tokens and in wrong first fixes.

## What ships

| Skill | Answers |
|---|---|
| `oberth-triage` | A run went red. What do I read, in what order, without spending a context window? |
| `oberth-pipeline` | How do I author `.oberth/build.yaml`, and what will the gate refuse and why? |
| `oberth-fragments` | How do I share a pipeline step between repositories, and what does pinning guarantee? |
| `oberth-release` | How does the credentialed tier differ, and how do secret paths get approved? |

Four separate skills rather than one document, because a skill's body loads when
it is used. One combined file would cost a context window whenever an agent
needed any part of it.

## Installing them

```
oberth skills list                    # names and descriptions
oberth skills show oberth-triage      # one body to stdout
oberth skills install                 # write into this repository
oberth skills install oberth-triage   # just one
```

All offline. The skills are in the binary, so none of this needs a server.

## Three targets, because the ecosystem has three shapes

| `--target` | Writes | Read by |
|---|---|---|
| `agents` | `AGENTS.md` at the repository root | Codex, Cursor, Copilot, Gemini CLI, Aider, Windsurf, Zed, Lovable, and others |
| `claude` | `.claude/skills/<name>/SKILL.md` | Claude Code, and other consumers of the Agent Skills format |
| `replit` | `custom_instruction/instructions.md` | Replit |

With no `--target`, Oberth picks from what the repository already contains and
says which it chose. `AGENTS.md` is the default when nothing is present: it is
the widest net and needs no frontmatter.

`--personal` writes to the home directory instead of the working directory.

## Your file stays yours

`AGENTS.md` is a file the repository may already own. Oberth writes only between
two markers:

```
<!-- BEGIN oberth skills -->
...
<!-- END oberth skills -->
```

Everything above and below is left byte-identical, and a second install updates
the region in place rather than appending a duplicate. A marker line inside a
fenced code block is treated as prose, not as a delimiter, so documentation
about the markers survives.

For `.claude/skills/<name>/SKILL.md` the whole file is Oberth's, but a file
Oberth did not write is refused rather than replaced. `--force` overrides that.
A symlinked destination is always refused.

## Over MCP

`skills` lists the catalogue and `skill_get` returns one body, so an agent
already talking to Oberth can read a skill without the CLI or the filesystem.

## Why they cannot drift

A test extracts every backticked token from every skill body and asserts the
flag, subcommand, path, environment variable or MCP tool name appears in this
binary's own source. A skill that describes a removed feature fails the build,
naming the skill and the token.

That matters more than it sounds. Documentation that is merely stale is a
nuisance; a skill that is stale is confident misinformation an agent will act
on.
