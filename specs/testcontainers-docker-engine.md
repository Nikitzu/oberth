# Spec: Testcontainers on the Docker engine

The kube engine offers Testcontainers through kubedock (`oberth install --testcontainers`): a pipeline sets `DOCKER_HOST=tcp://kubedock:2475` and test containers become pods. The Docker engine offers nothing, so every Java backend (coreapi-bookings and the rest, `testcontainers-postgresql`, `testcontainers-gcloud`) can only run on mikihome. This closes that gap on the laptop without changing a single pipeline document.

## Assumptions

1. The same `.oberth/build.yaml` must run on both engines unchanged. A pipeline that works on kube by naming `tcp://kubedock:2475` works on Docker by naming the same address.
2. Steps keep their baseline (`--cap-drop ALL`, `--read-only`, `no-new-privileges`, no socket). Access to a daemon goes through a proxy container that holds the socket, never through the step.
3. A proxy with an endpoint allowlist still lets a test create arbitrary containers on the daemon, including bind mounts of host paths. On a laptop that daemon is the developer's own, which is the accepted risk; the feature is off by default and opt-in twice, at install and per pipeline.
4. Ryuk (Testcontainers' reaper) needs a socket it will not get; Oberth reaps instead, by label, at run end.
5. Test containers publish their ports on the host, so the step reaches them at the daemon's host, not at the proxy. `TESTCONTAINERS_HOST_OVERRIDE` carries that.
6. OrbStack and Docker Desktop resolve `host.docker.internal`; Linux daemons need `--add-host ...:host-gateway`, which the engine already does for the secret store.

## Objective

`oberth install --engine=docker --testcontainers` on a laptop, then `oberth onboard` on coreapi-bookings, then a green run whose tests started Postgres through Testcontainers. `oberth status` says whether the deployment offers it. A pipeline that declares it on a server that does not offer it is refused at admission with one sentence, not at minute six with a connection timeout.

## Functional Requirements

**Install and status**

- WHERE `oberth install --engine=docker --testcontainers` is run THE SYSTEM SHALL persist `--testcontainers` into the serve invocation of the service unit, exactly as `--secretstore` is persisted today.
- WHEN the server starts with `--testcontainers` THE SYSTEM SHALL pull the proxy image (digest-pinned, `tecnativa/docker-socket-proxy`) before accepting runs and refuse to start if the daemon socket is not readable.
- THE SYSTEM SHALL report `testcontainers: offered | not offered` in `GET /api/status` and `oberth status` for both engines (kube reads `kubedock.enabled`).

**Declaring**

- THE SYSTEM SHALL read the workflow annotation `oberth.ci/testcontainers: "true"`.
- IF a workflow declares it and the deployment does not offer it, THEN THE SYSTEM SHALL refuse admission with `this pipeline needs Testcontainers and this server does not offer it (oberth install --testcontainers)`.
- IF a workflow does not declare it, THEN THE SYSTEM SHALL start no proxy and inject nothing, on either engine.

**The run, Docker engine**

- WHEN a declared run is provisioned THE SYSTEM SHALL start one proxy container on the run's network with the aliases `kubedock` and `docker`, listening on 2475 and 2375, the daemon socket mounted read-only into it and into nothing else.
- THE SYSTEM SHALL allow on the proxy only: ping, version, info, events, containers (list, create, start, stop, kill, remove, inspect, logs, wait, attach, archive, exec), images (list, inspect, pull, create), networks (list, create, connect, disconnect, remove); and SHALL deny build, commit, volumes, secrets, swarm, plugins, system prune.
- THE SYSTEM SHALL inject into every step of a declared run, unless the document already sets the variable: `DOCKER_HOST=tcp://kubedock:2475`, `TESTCONTAINERS_RYUK_DISABLED=true`, `TESTCONTAINERS_HOST_OVERRIDE=<host gateway name>`, `TESTCONTAINERS_CHECKS_DISABLE=true`.
- THE SYSTEM SHALL add `--add-host <host gateway name>:host-gateway` to every step of a declared run.
- THE SYSTEM SHALL label nothing on test containers (it cannot); instead, WHEN the run is provisioned THE SYSTEM SHALL record the ids of containers and networks carrying `org.testcontainers=true` that already exist.
- WHEN the run ends, green or red or cancelled, THE SYSTEM SHALL remove every container and network carrying `org.testcontainers=true` that is not in the recorded set, then the proxy container, and SHALL log what it removed with the run id. A removal failure is logged and never turns a green run red.
- WHILE the proxy is running THE SYSTEM SHALL cap it at 0.25 CPU and 64 MiB.
- THE SYSTEM SHALL count the proxy's lifetime inside the run's wall clock and its removal inside cleanup, so `oberth run <id>` shows one more container per run and nothing else changes.

**The run, kube engine**

- WHERE the annotation is declared THE SYSTEM SHALL behave as today (kubedock in the namespace), plus the admission refusal above when `kubedock.enabled` is false.

**Host gateway name**

- THE SYSTEM SHALL reuse `SecretStore.HostGatewayName` when set, else `host.docker.internal`, mapped with `--add-host <name>:host-gateway` and nothing else.
- IF the daemon reports a server version below 20.10, THEN `oberth install --testcontainers` SHALL refuse, naming the version and the 20.10 floor.

**Onboarding**

- WHEN `oberth onboard` generates a pipeline for a repository whose `pom.xml`, `build.gradle` or `build.gradle.kts` names a `testcontainers` dependency THE SYSTEM SHALL add `oberth.ci/testcontainers: "true"` to the generated document and say so in the onboard output.
- IF that server does not offer Testcontainers, THEN the store step SHALL fail with the admission sentence, before any push, and name the install flag.

**Diagnostics**

- WHEN a declared run fails at a step whose log contains `Could not find a valid Docker environment` or `connection refused` against 2475 THE SYSTEM SHALL append to that step's log one line: `testcontainers: the proxy was <running|exited code N>; see oberth log <id> --step oberth-testcontainers`.
- THE SYSTEM SHALL keep the proxy's own log as a pseudo-step `oberth-testcontainers` readable with `oberth log <id> --step oberth-testcontainers`.

## Tech Stack

Go, the Docker CLI as today (no SDK), `tecnativa/docker-socket-proxy` pinned by digest in `internal/dockerjob/testcontainers.go`. Chart untouched.

## Commands

```
Test:        go test ./internal/dockerjob/ ./internal/service/ ./cmd/oberth/
Vet:         go vet ./...
Build:       go build ./cmd/oberth
Release:     git tag v0.13.31-pocNN && git push origin v0.13.31-pocNN   (fork workflow builds and publishes)
Local run:   oberth install --engine=docker --secretstore --testcontainers --service --shell-profile=yes
Acceptance:  cd ~/Documents/coreapi-bookings && oberth onboard
```

## Project Structure

```
internal/dockerjob/testcontainers.go        → proxy image ref, allowlist env, start/stop, snapshot, reap
internal/dockerjob/testcontainers_test.go   → argv assertions (no daemon), reap set arithmetic
internal/dockerjob/controller.go            → Request.Testcontainers; provision starts proxy; createArguments injects env and add-host; cleanup reaps
internal/dockerjob/compile.go               → read the annotation into the Request
internal/app/dockerjobs.go                  → pass Config.Testcontainers through
internal/service/pipelines.go               → admission: declared but not offered
internal/api/status.go                      → testcontainers: offered | not offered
cmd/oberth/serve.go, docker.go              → --testcontainers flag into dockerjob.Config
cmd/oberth/install_docker.go                → persist the flag into the unit
cmd/oberth/status.go                        → print the line
internal/pipelinegen/project.go, generate.go → detect the testcontainers dependency, emit the annotation
internal/pipelinegen/generate_test.go       → pom and gradle fixtures
internal/skills/content/pipeline.md         → one paragraph: declare the annotation, same address on both engines
docs/getting-started.md                     → the install line
```

## Code Style

Match `createArguments`: the argv is built as a slice and asserted in tests without a daemon, because what the engine hands the CLI is the security boundary. Constants carry the reason in a one-line comment when the number is not obvious.

```go
// The proxy is the only container that sees the socket; a step reaches the
// daemon through the run network and this alias, which is the same name a
// kubedock pipeline already uses.
const proxyAlias = "kubedock"
```

## Testing Strategy

Go tests beside the code. Unit: proxy argv (socket mounted read-only, both aliases, both ports, resource caps, allowlist env exactly), step argv for a declared run (env injected, add-host present, document-set `DOCKER_HOST` respected), admission refusal text, reap set arithmetic (pre-existing ids survive, new ones go, networks too). Integration: the existing docker engine integration test, when a daemon is present, runs a step that does `docker run --rm alpine true` against the proxy and asserts the container is gone afterwards. Acceptance: coreapi-bookings green on the laptop through `oberth onboard`.

## Boundaries

- Always: keep the socket out of every step container; keep the feature off unless both the install flag and the annotation say yes; run `go test` and `go vet` before each commit; a run's own containers are labelled as today, test containers are reaped by `org.testcontainers=true` minus the snapshot.
- Ask first: adding an endpoint to the proxy allowlist beyond the list above (exec and build in particular); mounting the socket read-write; any change to the kube path beyond the admission refusal.
- Never: mount the socket into a step; run a step with `--privileged` or extra capabilities; reap containers that existed before the run; let a red reap fail a green run.

## Success Criteria

1. `oberth install --engine=docker --testcontainers` on the laptop, then `oberth status` prints `testcontainers: offered`; mikihome prints the same through kubedock.
2. coreapi-bookings onboarded on the laptop: run green, `oberth log <id> --step test` shows Postgres started by Testcontainers, `docker ps -a` shows no `org.testcontainers` containers or networks after the run.
3. A run of the same repository on a Docker server installed without the flag is refused at admission with the one-sentence message.
4. rides and tzmem (no annotation) run exactly as before: no proxy container appears, no env injected (`oberth log` shows no `DOCKER_HOST`).
5. Killing the test step mid-run leaves no `org.testcontainers` containers behind.
6. All existing dockerjob tests pass; new argv tests assert the socket is mounted only on the proxy.
7. `oberth onboard --dry-run` on coreapi-bookings prints the annotation in its step inventory; on rides it does not.

## Clarifications

1. `exec` is allowed on the proxy from the start: Kafka and several wait strategies use it, and a deny-then-widen loop costs a release per module.
2. Reaping is `org.testcontainers=true` minus the snapshot at run start, nothing finer. A Testcontainers run started by hand while the laptop server is mid-run is the accepted rare casualty; the simpler rule is the one to trust.
3. Host gateway is `--add-host <name>:host-gateway` only (Docker 20.10+); install refuses older daemons with the version named. No bridge-IP fallback.
4. `oberth onboard` detects a `testcontainers` dependency in Maven or Gradle and writes the annotation itself, so a Java repository stays one line to onboard.

## Open Questions

None.
