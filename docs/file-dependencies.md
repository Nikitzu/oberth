# Pinned file dependencies

A pipeline's workspace holds exactly one thing: the repository under test. When
a step needs shared data -- a registry, a policy list, an identifier map -- it
declares a file dependency and the server delivers it.

## Declaring one

```yaml
# .oberth/build.yaml
metadata:
  annotations:
    oberth.ci/files: |
      depgraph@v1:graph/repos.yml
      policy@v3:ci/allowed-images.txt
```

Entries separate on newlines or commas. Read them under `$OBERTH_FILES`:

```
/work/files/depgraph/graph/repos.yml
/work/files/policy/ci/allowed-images.txt
```

## Why the server reads it and the pipeline does not

The three obvious alternatives are all worse.

**Cloning it from Oberth** needs an uplink key, and an uplink key can promote to
main in every repository the server hosts. `sourcevolume.go` refuses that trade
for the checkout itself, and the same reasoning holds here.

**Vendoring it** gives every consuming repository its own copy to drift. The
file that decides whether a cross-repo claim is real is the worst candidate for
N stale copies.

**Fetching it at runtime** reintroduces an unpinned, unauditable input to a
build, which is the class of problem the admission gate exists to prevent.

So the server does the read, on its own side of the trust boundary, and the
pipeline receives bytes rather than a credential.

## Pinning

`repository@version:path`, where the version is always a tag. The
repository-and-version half is parsed by the same code that parses a fragment
reference, so a file dependency and a fragment are pinned by one rule rather
than two that can drift.

An input that can change under a build is an input that makes the build's
result unattributable to its inputs.

## What is recorded

On the submitted Workflow, `oberth.ci/files` holds the resolved lock rather
than what was written:

```json
[{"ref":{"repo":"frag-lib","version":"v1","path":"README.md"},
  "sha":"43b3c417b97ebc88b66273e48d53c4a6774b6cb4",
  "digest":"4a6689419b00b11700c9b6246bcfa8936c8f5e1e824db3a7e57030e2d1c1a684"}]
```

The digest is of the delivered bytes, so `sha256sum` inside a step and the
audit record can be compared. The submission audit action carries the same lock.

Build refuses a document whose declaration it could not resolve, which is why
one annotation is enough: there is no run in which the unresolved form survives
to be misread as a lock.

## Bounds and refusals

| Limit | Value |
|---|---|
| Files per pipeline | 8 |
| Bytes per file | 1 MiB |
| Bytes in total | 4 MiB |

Over any of these the run is refused, naming the file and the limit. Nothing is
truncated: a truncated registry answers "no" to entries that exist, which is
worse than not running.

A reference to a repository this server does not host is refused by name. When
a fragment allowlist is configured it governs file dependencies too -- both
answer one question, which repositories a pipeline may read content from at
build time, and two lists would drift.

## Delivery

The bytes ride the run's existing source claim as a fifth subPath, mounted
read-only at `/work/files`, and are delivered to every run rather than only
credentialed ones: the trust anchor and the server binary are gated on
credentials because both are about identity, and declared file content is not.

The mount sits outside `/work/src` for the reason the trust anchor does. A
repository sees its own tree at the checkout and nothing the server added to
it, and cannot shadow a delivered file with one of its own.

## Inspecting

```
oberth files show depgraph@v1:graph/repos.yml
```

In-pod, reading the git cache directly, needing no running server.

There is no `oberth files list`. Enumerating a tag's tree needs a `git ls-tree`
the cache does not have, with its own bounds question, and a reference is
written by someone who already knows the path.

`oberth validate` lists declared references and still checks admission. A
fragment reference stops that check because unresolved templates leave the
document structurally incomplete; a file dependency changes no template, so
what validate admits is what will run.
