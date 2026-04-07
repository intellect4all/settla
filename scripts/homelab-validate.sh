#!/usr/bin/env bash
# Homelab post-deploy validation script.
# Run after deploying the homelab overlay to verify the cluster is healthy.
#
# Usage: ./scripts/homelab-validate.sh [--load-test]
#   --load-test: Also run Scenario B (580 TPS / 10 min) after validation checks.

set -euo pipefail

NAMESPACE="${NAMESPACE:-settla}"
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}[PASS]${NC} $1"; }
fail() { echo -e "${RED}[FAIL]${NC} $1"; FAILURES=$((FAILURES + 1)); }
warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }

FAILURES=0

echo "=== Settla Homelab Validation ==="
echo "Namespace: ${NAMESPACE}"
echo ""

# --- 1. Node health ---
echo "--- Node Health ---"
NODE_COUNT=$(kubectl get nodes --no-headers 2>/dev/null | grep -c " Ready" || true)
if [ "$NODE_COUNT" -ge 2 ]; then
  pass "Cluster has ${NODE_COUNT} Ready nodes"
else
  fail "Cluster has ${NODE_COUNT} Ready nodes (need at least 2)"
fi

# Check node labels
for node in $(kubectl get nodes -o name 2>/dev/null); do
  ROLE=$(kubectl get "$node" -o jsonpath='{.metadata.labels.settla\.io/role}' 2>/dev/null || true)
  if [ -n "$ROLE" ]; then
    pass "$node labeled settla.io/role=${ROLE}"
  else
    warn "$node missing settla.io/role label"
  fi
done

# --- 2. Node resources ---
echo ""
echo "--- Node Resources ---"
kubectl top nodes 2>/dev/null || warn "Metrics server not available (kubectl top nodes failed)"

for node in $(kubectl get nodes -o jsonpath='{.items[*].metadata.name}' 2>/dev/null); do
  CPU_PCT=$(kubectl top node "$node" --no-headers 2>/dev/null | awk '{print $3}' | tr -d '%' || echo "0")
  MEM_PCT=$(kubectl top node "$node" --no-headers 2>/dev/null | awk '{print $5}' | tr -d '%' || echo "0")
  if [ "${CPU_PCT:-0}" -le 80 ]; then
    pass "$node CPU: ${CPU_PCT}% (target: <80%)"
  else
    fail "$node CPU: ${CPU_PCT}% exceeds 80%"
  fi
  if [ "${MEM_PCT:-0}" -le 50 ]; then
    pass "$node Memory: ${MEM_PCT}% (target: <50%)"
  else
    warn "$node Memory: ${MEM_PCT}% exceeds 50% target"
  fi
done

# --- 3. Pod status ---
echo ""
echo "--- Pod Status ---"
NOT_RUNNING=$(kubectl get pods -n "$NAMESPACE" --no-headers 2>/dev/null | grep -v "Running\|Completed" | wc -l | tr -d ' ')
if [ "$NOT_RUNNING" -eq 0 ]; then
  pass "All pods are Running"
else
  fail "${NOT_RUNNING} pods are not Running:"
  kubectl get pods -n "$NAMESPACE" --no-headers 2>/dev/null | grep -v "Running\|Completed"
fi

# Check for OOMKill or CrashLoopBackOff
OOMKILL=$(kubectl get pods -n "$NAMESPACE" -o jsonpath='{range .items[*]}{range .status.containerStatuses[*]}{.lastState.terminated.reason}{"\n"}{end}{end}' 2>/dev/null | grep -c "OOMKilled" || true)
if [ "$OOMKILL" -gt 0 ]; then
  fail "${OOMKILL} containers have been OOMKilled"
else
  pass "No OOMKilled containers"
fi

# Check pod spread across nodes
echo ""
echo "--- Pod Spread ---"
kubectl get pods -n "$NAMESPACE" -o wide --no-headers 2>/dev/null | awk '{print $7}' | sort | uniq -c | sort -rn | while read -r count node; do
  echo "  ${node}: ${count} pods"
done

# Verify settla-server spread
SERVER_NODES=$(kubectl get pods -n "$NAMESPACE" -l app.kubernetes.io/name=settla-server -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' 2>/dev/null | sort -u | wc -l | tr -d ' ')
SERVER_REPLICAS=$(kubectl get pods -n "$NAMESPACE" -l app.kubernetes.io/name=settla-server --no-headers 2>/dev/null | wc -l | tr -d ' ')
if [ "$SERVER_NODES" -ge 2 ] || [ "$NODE_COUNT" -le 2 ]; then
  pass "settla-server (${SERVER_REPLICAS} replicas) spread across ${SERVER_NODES} nodes"
else
  warn "settla-server (${SERVER_REPLICAS} replicas) on only ${SERVER_NODES} node(s)"
fi

# --- 4. Service connectivity ---
echo ""
echo "--- Service Connectivity ---"
for svc in gateway nats redis pgbouncer-ledger pgbouncer-transfer pgbouncer-treasury; do
  ENDPOINTS=$(kubectl get endpoints "$svc" -n "$NAMESPACE" -o jsonpath='{.subsets[*].addresses[*].ip}' 2>/dev/null || true)
  if [ -n "$ENDPOINTS" ]; then
    pass "${svc} has endpoints"
  else
    fail "${svc} has no endpoints"
  fi
done

# --- 5. PVC status ---
echo ""
echo "--- PVC Status ---"
PENDING_PVC=$(kubectl get pvc -n "$NAMESPACE" --no-headers 2>/dev/null | grep -c "Pending" || true)
if [ "$PENDING_PVC" -eq 0 ]; then
  pass "All PVCs are Bound"
else
  fail "${PENDING_PVC} PVCs are Pending:"
  kubectl get pvc -n "$NAMESPACE" --no-headers 2>/dev/null | grep "Pending"
fi

# --- 6. NATS JetStream check ---
echo ""
echo "--- NATS JetStream ---"
NATS_POD=$(kubectl get pods -n "$NAMESPACE" -l app.kubernetes.io/name=nats -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
if [ -n "$NATS_POD" ]; then
  NATS_HEALTH=$(kubectl exec -n "$NAMESPACE" "$NATS_POD" -- wget -qO- http://localhost:8222/healthz 2>/dev/null || echo "unhealthy")
  if echo "$NATS_HEALTH" | grep -qi "ok\|status.*ok"; then
    pass "NATS JetStream is healthy"
  else
    warn "NATS JetStream health check: ${NATS_HEALTH}"
  fi
else
  fail "No NATS pod found"
fi

# --- 7. External data plane connectivity ---
echo ""
echo "--- External Data Plane ---"
GATEWAY_POD=$(kubectl get pods -n "$NAMESPACE" -l app.kubernetes.io/name=gateway -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
if [ -n "$GATEWAY_POD" ]; then
  # Test PgBouncer->external Postgres connectivity via gateway pod
  for db in pgbouncer-ledger pgbouncer-transfer pgbouncer-treasury; do
    if kubectl exec -n "$NAMESPACE" "$GATEWAY_POD" -- sh -c "nc -z -w3 ${db} 6432" 2>/dev/null; then
      pass "Can reach ${db}:6432 from cluster"
    else
      warn "Cannot reach ${db}:6432 from cluster (PgBouncer may not be routing yet)"
    fi
  done
fi

# --- Summary ---
echo ""
echo "=== Summary ==="
if [ "$FAILURES" -eq 0 ]; then
  pass "All checks passed"
else
  fail "${FAILURES} check(s) failed"
fi

# --- Optional load test ---
if [ "${1:-}" = "--load-test" ]; then
  echo ""
  echo "=== Running Scenario B: 580 TPS / 10 min ==="
  NODE_IP=$(kubectl get nodes -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}' 2>/dev/null)
  GATEWAY_URL="http://${NODE_IP}:30080"
  echo "Gateway URL: ${GATEWAY_URL}"
  make bench-sustained GATEWAY_URL="${GATEWAY_URL}"
fi

exit "$FAILURES"
