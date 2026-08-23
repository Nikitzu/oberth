#!/bin/bash
set -euo pipefail

NAMESPACE=quickstart-test
VAULT_ADDR="http://qs-secretstore-svc.${NAMESPACE}.svc.cluster.local:8200"
VAULT_TOKEN=root
BEACON_ADDR="qs-beacon-svc.${NAMESPACE}.svc.cluster.local:443"

echo "=== Quickstart Test: waiting for VMs ==="

# Wait for cloud-init to complete on all 3 VMs
wait_vm_setup() {
  local vm=$1
  local timeout=300
  local elapsed=0
  echo "Waiting for ${vm} cloud-init..."
  while [ $elapsed -lt $timeout ]; do
    if virtctl ssh --local-ssh -n "${NAMESPACE}" "dev@${vm}" --command "test -f /tmp/setup-done" 2>/dev/null; then
      echo "${vm}: setup done (${elapsed}s)"
      return 0
    fi
    sleep 5
    elapsed=$((elapsed + 5))
  done
  echo "FAIL: ${vm} cloud-init did not complete in ${timeout}s"
  return 1
}

wait_vm_setup qs-secretstore
wait_vm_setup qs-beacon
wait_vm_setup qs-target

echo "=== All VMs ready ==="

# Helper to run commands on target VM
target_exec() {
  virtctl ssh --local-ssh -n "${NAMESPACE}" "dev@qs-target" --command "$1" 2>&1
}

# Step 1: Verify CLI installed on target
echo "=== Step 1: Verify CLI ==="
target_exec "cloudtaser-cli version"

# Step 2: Install operator via helm on target's k3s
echo "=== Step 2: Install operator ==="
target_exec "helm repo add cloudtaser https://charts.cloudtaser.io && helm repo update"
target_exec "helm install cloudtaser cloudtaser/cloudtaser --namespace cloudtaser-system --create-namespace --set operator.logLevel=debug --set operator.broker.beacon.enabled=false"

# Wait for operator to be ready
echo "Waiting for operator deployment..."
target_exec "kubectl rollout status deployment/cloudtaser-operator -n cloudtaser-system --timeout=120s"

# Step 3: Connect cluster to secret store
echo "=== Step 3: target install ==="
target_exec "cloudtaser-cli target install --vault-addr ${VAULT_ADDR} --vault-token ${VAULT_TOKEN} --beacon-addr ${BEACON_ADDR} --mode p2p --non-interactive"

# Step 4: Store test secret via vault HTTP API
echo "=== Step 4: Store test secret ==="
curl -sf -H "X-Vault-Token: ${VAULT_TOKEN}" \
  -X POST -d '{"data":{"password":"s3cret","username":"app_user"}}' \
  "${VAULT_ADDR}/v1/secret/data/quickstart/db"
echo "Secret stored in vault"

# Step 5: Deploy annotated test pod on target's k3s
echo "=== Step 5: Deploy test workload ==="
target_exec 'kubectl apply -f - <<PODEOF
apiVersion: v1
kind: Pod
metadata:
  name: quickstart-test-pod
  namespace: default
  annotations:
    cloudtaser.io/inject: "true"
    cloudtaser.io/secret-paths: "secret/data/quickstart/db"
    cloudtaser.io/env-map: "password=PGPASSWORD,username=PGUSER"
spec:
  containers:
  - name: app
    image: ubuntu:22.04
    command: ["sleep", "300"]
PODEOF'

# Wait for pod to be ready
echo "Waiting for test pod..."
target_exec "kubectl wait pod/quickstart-test-pod --for=condition=Ready --timeout=180s"

# Step 6a: Secret NOT in /proc/1/environ (eBPF enforcement)
echo "=== Step 6a: Verify eBPF enforcement ==="
ENVIRON_OUTPUT=$(target_exec "kubectl exec quickstart-test-pod -- cat /proc/1/environ 2>&1" || true)
if echo "${ENVIRON_OUTPUT}" | tr '\0' '\n' | grep -q "PGPASSWORD"; then
  echo "FAIL: PGPASSWORD found in /proc/1/environ - eBPF enforcement not working"
  exit 1
fi
echo "PASS: secret not in /proc/1/environ"

# Step 6b: Secret IS accessible via environment (injected by wrapper)
echo "=== Step 6b: Verify secret injection ==="
ENV_OUTPUT=$(target_exec "kubectl exec quickstart-test-pod -- printenv PGPASSWORD" || true)
if [ -z "${ENV_OUTPUT}" ] || [ "${ENV_OUTPUT}" = "" ]; then
  echo "FAIL: PGPASSWORD not available in pod env"
  exit 1
fi
echo "PASS: secret injected into pod env"

# Step 6c: Verify target status
echo "=== Step 6c: Target status ==="
target_exec "cloudtaser-cli target status"

echo ""
echo "=== Quickstart test PASSED ==="
