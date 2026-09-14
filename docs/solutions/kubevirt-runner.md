# Trusted Isolated KubeVirt Runner

Design for adding an operator-owned VM runner/controller that provides
bounded, least-privilege KubeVirt-based execution for CloudTaser integration
test suites (Oberth issue #278, blocking epic #277).

## Problem

Oberth's execution model creates Argo Workflows in a pipeline namespace where
every container runs under a server-assigned security baseline. This model is
correct for building and releasing software but cannot host workloads that
require real VM isolation: CloudTaser's own integration suites need k3s,
systemd, FUSE, eBPF, and PostgreSQL (which refuses root) inside a guest OS
that carries `/dev/kvm`. Five structural barriers in the current model block
this:

| Barrier | Location | Why it exists |
|---------|----------|---------------|
| Resource templates rejected | `pkg/argoworkflow/admit.go:324` | A resource template creates arbitrary Kubernetes objects using the workflow's own ServiceAccount -- on the release tier that is a direct privilege escalation |
| Repository security contexts rejected | `pkg/argoworkflow/admit.go:620` | The server forces the entire container baseline; a repository-authored context is either a lie or a weakening |
| UID 0 with capabilities dropped | `internal/argojob/spec.go:908` | PostgreSQL initdb and systemd refuse root; the baseline cannot be relaxed per-step |
| No Kubernetes token for branch steps | `internal/argojob/spec.go` | Branch-tier pods run `automountServiceAccountToken: false`; they cannot create KubeVirt objects |
| No VM lifecycle management | (absent) | No component to create, monitor, bound, or clean up VirtualMachineInstances |

Historical issues #201/#202 were closed because the old canary dispatch was
unreachable, not because the underlying defects were fixed. Reusing old
templates verbatim is rejected by admission.

### What KubeVirt presence alone does not establish

Having KubeVirt deployed and `/dev/kvm` available on the node is necessary
infrastructure but establishes none of the following:

1. **No candidate-SHA binding:** nothing ties the guest workload to the exact
   source revision being tested.
2. **No suite revision pinning:** a candidate could modify the tests that
   evaluate it.
3. **No guest image pinning:** a mutable image tag can drift between the
   admission decision and the VM boot.
4. **No lifecycle bounds:** no bounded deadline, no automatic cleanup, no
   operator-owned cancellation.
5. **No trusted evidence:** an exit code printed as a log marker by code
   running inside the VM is as trustworthy as the VM's own integrity, which
   is to say: not at all.
6. **No credential isolation:** a pipeline container that can create arbitrary
   VMIs can label them to match any network policy, mount any Secret the
   service account can read, and escape the pipeline sandbox entirely.

## Design: operator-owned VM runner

### Architecture

The VM runner is a server-side controller that manages KubeVirt
VirtualMachineInstance (VMI) objects on behalf of pipeline steps. The pipeline
declares what to test; the server decides how to run it.

```
Pipeline step            Oberth server               KubeVirt
  declares               (operator-owned)
  VM run spec
      |                        |
      +-- admission --------->-+
      |   (validate spec)      |
      |                        +-- Create VMI -------->
      |                        |   (pinned image,      |
      |                        |    no SA token,        |
      |                        |    resource bounds)    |
      |                        |                        |
      |                        +-- Poll VMI phase ---->-+
      |                        |   (bounded deadline)   |
      |                        |                        |
      |                        +<-- VMI Succeeded/Failed
      |                        |
      +<-- VMRunResult --------+
          (exit code from VMI phase,
           server-collected evidence)
```

### Trust model

The trust boundary runs between the pipeline namespace (untrusted) and the
server process (operator-owned). Three invariants:

**I1: The candidate cannot modify the test suite.** The suite revision is
bound at admission time and resolved by the server from a trusted source (the
Oberth git cache, not the candidate's checkout). A candidate that tries to
alter the suite SHA in its own document gets a different document digest and
fails the identity check.

**I2: Evidence is server-collected, not guest-reported.** The VMI's exit code
comes from the KubeVirt API (the `Succeeded`/`Failed` VMI phase), not from a
log marker printed inside the guest. The server reads the phase through its
own Kubernetes client, not through a channel the guest controls.

**I3: The guest has no cluster access.** The VMI runs with no service account
token, no access to the Kubernetes API, and no network path to Oberth's own
namespace. A guest that escapes KubeVirt's QEMU isolation reaches only the
node's kernel, not the cluster control plane.

### VM run specification

A pipeline declares a VM-backed step through a typed specification:

```go
type VMRunSpec struct {
    // RunID links this VM execution to its parent Oberth run.
    RunID string

    // Repo is the repository under test.
    Repo string

    // CandidateSHA is the exact source commit whose artifact is under test.
    // 40-character lowercase hex, validated at admission.
    CandidateSHA string

    // SuiteRevision is the test suite's pinned commit, resolved independently
    // of the candidate's checkout. This is the defence against a candidate
    // modifying the tests that evaluate it.
    SuiteRevision string

    // GuestImageRef is the VM's boot image, which must be an OCI reference
    // pinned by digest (sha256:...). A tag-only reference is rejected at
    // admission because it can drift between the decision and the boot.
    GuestImageRef string

    // Resources caps the VM's allocation.
    Resources VMResources

    // Deadline is the maximum wall time. Positive, bounded by the server's
    // configured ceiling.
    Deadline time.Duration
}
```

### Resource bounds

```go
type VMResources struct {
    CPUCores  int    // 1..MaxVMCPUCores (default ceiling: 4)
    MemoryMiB int    // 512..MaxVMMemoryMiB (default ceiling: 16384)
    DiskGiB   int    // 0..MaxVMDiskGiB (default ceiling: 64)
}
```

### Admission validation

Every field is validated before a VMI exists:

| Field | Rule | Why |
|-------|------|-----|
| CandidateSHA | 40-char lowercase hex | Prevents injection, ensures exact commit binding |
| SuiteRevision | 40-char lowercase hex | Same; resolved from trusted source |
| GuestImageRef | Valid OCI reference with `@sha256:` digest | Tag-only references drift; digest pins the exact bytes |
| CPUCores | 1..ceiling | Prevents host exhaustion |
| MemoryMiB | 512..ceiling | KubeVirt minimum + ceiling |
| DiskGiB | 0..ceiling | Bounded ephemeral storage |
| Deadline | 1m..ceiling | Prevents unbounded occupation |
| RunID | Non-empty | Links evidence to the parent run |
| Repo | Non-empty | Scopes the execution |

### VMI construction (server-side only)

The server constructs the VMI with these fixed properties:

- **Namespace:** The pipeline namespace (same as Argo workflows), never the
  server namespace.
- **Name:** Derived deterministically from the run ID (collision-proof, same
  pattern as Workflow names).
- **Labels:** `oberth.ci/run-id`, `oberth.ci/repo`, `oberth.ci/trigger`,
  `oberth.ci/tier=vm-runner`.
- **No ServiceAccount token:** `automountServiceAccountToken: false`.
- **No host access:** No `hostNetwork`, `hostPID`, `hostIPC`, `hostPath`.
- **Resource bounds:** CPU and memory from the validated spec.
- **Deadline:** VMI's `terminationGracePeriodSeconds` plus a server-side
  context deadline.
- **Guest image:** The exact digest-pinned reference from the spec. The
  KubeVirt containerDisk source uses the OCI image directly.

### Evidence collection

The server collects evidence from three sources, none of which pass through
the guest:

1. **VMI phase:** `Succeeded` (guest exited 0) or `Failed` (guest exited
   non-zero or timed out). Read from the KubeVirt API by the server's own
   client.
2. **VMI metadata:** UID, creation timestamp, completion timestamp. Prove
   which object ran and when.
3. **Binding echo:** The candidate SHA, suite revision, and guest image digest
   are echoed from the spec that was admitted, not from anything the guest
   reports.

```go
type VMRunResult struct {
    Phase         string    // "Succeeded" or "Failed"
    ExitCode      int32     // From VMI status, not a log marker
    Evidence      VMEvidence
    Duration      time.Duration
}

type VMEvidence struct {
    CandidateSHA     string
    SuiteRevision    string
    GuestImageRef    string
    VMInstanceName   string
    VMInstanceUID    string
    StartedAt        time.Time
    FinishedAt       time.Time
    ExitCodeSource   string // "vmi-phase": server-read; never "log-marker"
}
```

### Lifecycle management

```
Create  ->  Running  ->  Succeeded/Failed  ->  Cleanup
  |            |               |
  |            +-- deadline -->-+ (server cancels)
  |                            |
  +-- context cancel -------->-+ (server deletes VMI)
```

**Cleanup contract:** The server deletes the VMI and its associated resources
(PVCs, if any) in a `context.WithoutCancel` cleanup path, the same pattern
the Argo engine uses for claim cleanup. An unfinished VMI is never left
running after the server stops monitoring it.

### Adversarial scenarios and defences

| Attack | Defence | Test |
|--------|---------|------|
| Candidate modifies suite revision in its own document | Suite revision is admission-bound; document identity changes | `TestVMRunSpec_CandidateCannotModifySuiteRevision` |
| Guest prints fake exit code markers | Evidence is from VMI phase, not log markers; `ExitCodeSource` is always `vmi-phase` | `TestEvidence_ExitCodeSourceNeverLogMarker` |
| Guest creates Kubernetes objects | No ServiceAccount token mounted; API server rejects unauthenticated requests | `TestVMI_NoServiceAccountToken` |
| Guest accesses Oberth namespace | NetworkPolicy restricts pipeline namespace; VMI has no cluster-admin | `TestVMI_NoServerNamespaceAccess` |
| Candidate uses tag-only guest image | Admission rejects references without `@sha256:` digest | `TestVMRunSpec_RejectsTagOnlyImage` |
| Candidate requests unbounded resources | Admission enforces ceilings on CPU, memory, disk, deadline | `TestVMRunSpec_RejectsExcessiveResources` |
| Race between admission and VMI creation | Server constructs VMI from the admitted spec, not from a re-read | Structural: no second read path |
| Orphaned VMI after server crash | Sweep function lists VMIs by label, deletes those past grace window | `TestController_SweepOrphanedVMIs` |

### Nonroot execution (PostgreSQL lane)

PostgreSQL's initdb refuses UID 0. The existing nonroot execution profile
(`nonroot-static-v1`) already provides a verified UID 65534 lane for
container/script leaves. For VM-based PostgreSQL:

- The guest OS inside the KubeVirt VM runs its own user namespace. PostgreSQL
  runs as `postgres` (UID 999 or 70 depending on the image) inside the guest,
  which is mapped to a non-root UID on the host by KubeVirt's domain isolation.
- No change to Oberth's container security baseline is needed: the VMI is not
  a container step, and its internal UID mapping is the guest OS's concern.

For non-VM PostgreSQL in ordinary container steps, the existing nonroot
profile applies. This is independent of the VM runner.

### ARM execution

KubeVirt VMIs inherit the host node's architecture. On an ARM node with
`/dev/kvm`, the VMI runs native ARM code. No cross-architecture emulation is
needed or supported. The guest image must match the node architecture; the
admission validates this by requiring a digest-pinned image (which is
architecture-specific by construction for single-platform images, or the
correct platform child of a multi-arch index).

### Integration with existing Oberth components

The VM runner does not replace the Argo workflow engine. It is a parallel
execution path for steps that need VM isolation:

- **Scheduler:** Routes a step to the VM runner when the step's template
  carries the `oberth.ci/vm-runner` annotation.
- **Run progress:** VM run results are reported through the same progress
  marker protocol the Argo engine uses (`runprogress.Event`).
- **Run log:** VM console output is collected server-side and written to the
  run log with the standard `[burn/step]` prefix.
- **Cleanup:** Integrates with the existing orphan sweep pattern.

### Dependencies

- **KubeVirt CRDs:** Must be installed in the cluster. The controller uses
  the KubeVirt API (`kubevirt.io/api`) to create VMIs.
- **`/dev/kvm`:** Must be present on the node for hardware-accelerated
  execution. Without it, KubeVirt falls back to TCG emulation (supported
  but much slower).
- **Network policy:** The pipeline namespace needs a policy that allows VMI
  pod-to-pod communication within the namespace but blocks access to the
  server namespace and the Kubernetes API.

### Implementation phases

**Phase 1 (this change):**
- Design document (this file)
- Core types: `VMRunSpec`, `VMRunResult`, `VMEvidence`, `VMResources`
- Admission validation with complete bounds checking
- Trust binding: immutable spec identity, candidate/suite/image pinning
- Controller skeleton: interface and lifecycle state machine
- Adversarial negative tests: all scenarios from the table above

**Phase 2:**
- KubeVirt client integration (VMI CRUD operations)
- Live VMI lifecycle management (create, poll, cancel, cleanup)
- Console log collection and run-log integration
- Orphan VMI sweep

**Phase 3:**
- Scheduler integration (route steps to VM runner by annotation)
- Run progress integration (progress markers from VM runs)
- End-to-end test with a real KubeVirt workload

**Phase 4:**
- ARM node support
- Guest image build pipeline (digest-pinned containerDisk images)
- PostgreSQL integration test suite
- CTTV full-stack validation

### Success criteria

1. Every VMRunSpec field is validated at admission before any VMI exists
2. A candidate cannot modify the test suite that evaluates it
3. Evidence comes from the server's Kubernetes client, never from guest output
4. The guest has no access to cluster credentials or the server namespace
5. Resource bounds are enforced and prevent host exhaustion
6. Orphaned VMIs are cleaned up within a bounded grace window
7. All adversarial tests pass and cover the threat table above

### Non-goals

- Replacing the Argo workflow engine for ordinary build/release steps
- Running CloudTaser's eBPF kernel-variant matrix (that is the QEMU/virtme-ng
  path, see `docs/solutions/kernel-e2e-bridge.md`)
- Multi-tenant VM scheduling across nodes
- Persistent VM instances (every VMI is ephemeral and run-scoped)
- GPU passthrough or SR-IOV

## References

- Oberth issue #278: This issue
- Oberth issue #277: Parent epic
- Oberth issues #201, #202: Historical canary dispatch (closed, not fixed)
- `docs/solutions/kernel-e2e-bridge.md`: QEMU/virtme-ng path for kernel testing
- `pkg/argoworkflow/admit.go`: Admission gate
- `internal/argojob/spec.go`: Container security baseline
- `internal/argojob/nonroot.go`: Nonroot execution profile (pattern reference)
- `internal/argojob/nonroot_verifier.go`: Trust verification model
