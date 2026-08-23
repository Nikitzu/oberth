# Quickstart Test

Automated test for the TLDR quickstart flow (cloudtaser-docs#14).

Spins up three KubeVirt VMs that replicate the three-machine quickstart
topology (secret store, beacon relay, target cluster) and runs the full
6-step flow: CLI verify, operator install, target install, secret store,
workload deploy, injection + enforcement verify.

## Prerequisites

- KubeVirt and CDI installed on the host k3s cluster
- `virtctl` available in the test runner

## Optional: pre-cache the base image

Each DataVolume imports the Ubuntu Noble cloud image directly via HTTP.
If you want to avoid repeated downloads, you can pre-cache a shared base
image in the `kubevirt` namespace:

```bash
kubectl apply -f base-image.yaml
```

This is NOT required -- the quickstart test works without it.

## VM sizing

| VM | vCPU | Memory | Disk | Role |
|----|------|--------|------|------|
| qs-secretstore | 2 | 1.5 Gi | 5 Gi | OpenBao dev server + cloudtaser-port |
| qs-beacon | 1 | 512 Mi | 4 Gi | CloudTaser beacon relay |
| qs-target | 2 | 3 Gi | 12 Gi | k3s cluster + operator + test workload |

All VMs are scheduled on `clonebox` via nodeSelector.

## How it runs

The Argo WorkflowTemplate `docs-quickstart-test` applies the manifests,
starts the VMs, runs `test.sh`, and tears down the namespace on completion.
VMs are created with `runStrategy: Always` and boot as soon as the
manifests are applied; the workflow waits for readiness rather than
starting them.
