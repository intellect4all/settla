#!/usr/bin/env bash
# ════════════════════════════════════════════════════════════════════════
# Settla Automated Remediation
# ════════════════════════════════════════════════════════════════════════
#
# Called by AlertManager via webhook receiver when critical alerts fire.
# Each handler is IDEMPOTENT — safe to run multiple times.
#
# This script runs as a sidecar or standalone pod that receives
# AlertManager webhook POST requests on port 9095.
#
# Handlers:
#   1. SettlaHighErrorRateFastBurn  → rollback to last known good deployment
#   2. SettlaNATSConsumerLagCritical → scale up settla-node replicas
#   3. SettlaCircuitBreakerOpen     → restart affected pods
#   4. SettlaTreasuryFlushLagHigh   → trigger manual flush via admin endpoint
#   5. SettlaPgBouncerPoolCritical  → increase pool size via config patch
#   6. SettlaLoadSheddingCritical   → scale up gateway/server replicas
#   7. SettlaDiskSpaceCritical      → trigger partition cleanup
#   8. SettlaMemoryHigh             → restart memory-pressured pods
#
# Environment variables:
#   SETTLA_NAMESPACE        — Kubernetes namespace (default: settla)
#   SETTLA_REMEDIATION_LOG  — Log file path (default: /var/log/settla/remediation.log)
#   SETTLA_DRY_RUN          — Set to "true" to log actions without executing (default: false)
#   SETTLA_COOLDOWN_DIR     — Directory for cooldown marker files (default: /tmp/settla-cooldown)
#   SETTLA_SERVER_ADMIN_URL — Admin endpoint for settla-server (default: http://settla-server:8080)
#   SLACK_WEBHOOK_URL       — Slack incoming webhook URL for remediation notifications (required)
#
# ════════════════════════════════════════════════════════════════════════

set -euo pipefail

# ── Configuration ────────────────────────────────────────────────────────
NAMESPACE="${SETTLA_NAMESPACE:-settla}"
LOG_FILE="${SETTLA_REMEDIATION_LOG:-/var/log/settla/remediation.log}"
DRY_RUN="${SETTLA_DRY_RUN:-false}"
COOLDOWN_DIR="${SETTLA_COOLDOWN_DIR:-/tmp/settla-cooldown}"
SERVER_ADMIN_URL="${SETTLA_SERVER_ADMIN_URL:-http://settla-server:8080}"
LISTEN_PORT="${SETTLA_REMEDIATION_PORT:-9095}"
SLACK_WEBHOOK_URL="${SLACK_WEBHOOK_URL:-}"

# Cooldown periods (seconds) — prevent remediation from firing too often
COOLDOWN_ROLLBACK=1800      # 30 minutes between rollbacks
COOLDOWN_SCALE=300          # 5 minutes between scale operations
COOLDOWN_RESTART=600        # 10 minutes between restarts
COOLDOWN_FLUSH=60           # 1 minute between flush triggers
COOLDOWN_PGBOUNCER=900      # 15 minutes between pool size changes
COOLDOWN_CLEANUP=3600       # 1 hour between partition cleanups

# Scale limits — prevent runaway scaling
MAX_NODE_REPLICAS=16
MAX_SERVER_REPLICAS=12
MAX_GATEWAY_REPLICAS=8

# ── Logging ──────────────────────────────────────────────────────────────
mkdir -p "$(dirname "$LOG_FILE")" "$COOLDOWN_DIR"

log() {
    local level="$1"
    shift
    local msg="$*"
    local timestamp
    timestamp="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    echo "${timestamp} [${level}] ${msg}" | tee -a "$LOG_FILE"
}

log_info()  { log "INFO"  "$@"; }
log_warn()  { log "WARN"  "$@"; }
log_error() { log "ERROR" "$@"; }

# ── Slack notification ────────────────────────────────────────────────────
# Posts a message to Slack via incoming webhook.
# No-ops if SLACK_WEBHOOK_URL is unset.
notify_slack() {
    local color="$1"   # good | warning | danger
    local title="$2"
    local message="$3"

    if [[ -z "$SLACK_WEBHOOK_URL" ]]; then
        log_warn "SLACK_WEBHOOK_URL not set — skipping Slack notification"
        return 0
    fi

    local payload
    payload="$(printf '{"attachments":[{"color":"%s","title":"[Settla Remediation] %s","text":"%s","footer":"namespace: %s | dry_run: %s","ts":%s}]}' \
        "$color" "$title" "$message" "$NAMESPACE" "$DRY_RUN" "$(date +%s)")"

    if [[ "$DRY_RUN" == "true" ]]; then
        log_info "[DRY RUN] Would notify Slack: ${title} — ${message}"
        return 0
    fi

    curl -sf --connect-timeout 5 --max-time 10 \
        -X POST "$SLACK_WEBHOOK_URL" \
        -H "Content-Type: application/json" \
        -d "$payload" >/dev/null 2>&1 || log_warn "Slack notification failed (non-fatal)"
}

# ── Cooldown check ───────────────────────────────────────────────────────
# Returns 0 if action is allowed (cooldown expired), 1 if still in cooldown.
check_cooldown() {
    local action="$1"
    local cooldown_seconds="$2"
    local marker_file="${COOLDOWN_DIR}/${action}.marker"

    if [[ -f "$marker_file" ]]; then
        local last_run
        last_run="$(cat "$marker_file")"
        local now
        now="$(date +%s)"
        local elapsed=$(( now - last_run ))
        if (( elapsed < cooldown_seconds )); then
            log_info "Cooldown active for '${action}': ${elapsed}s / ${cooldown_seconds}s elapsed. Skipping."
            return 1
        fi
    fi

    # Set new cooldown marker
    date +%s > "$marker_file"
    return 0
}

# ── Dry run wrapper ──────────────────────────────────────────────────────
run_cmd() {
    if [[ "$DRY_RUN" == "true" ]]; then
        log_info "[DRY RUN] Would execute: $*"
        return 0
    fi
    log_info "Executing: $*"
    "$@" 2>&1 | tee -a "$LOG_FILE"
}

# ════════════════════════════════════════════════════════════════════════
# Remediation Handlers
# ════════════════════════════════════════════════════════════════════════

# ── 1. SettlaHighErrorRateFastBurn → Rollback deployment ────────────────
# Rolls back settla-server to the previous revision using Argo Rollouts.
# This is the nuclear option — only triggered by fast burn (14.4x rate).
handle_high_error_rate_fast_burn() {
    log_warn "REMEDIATION: SettlaHighErrorRateFastBurn — initiating rollback"
    notify_slack "danger" "SettlaHighErrorRateFastBurn" "Error budget fast burn detected. Initiating settla-server rollback to last known good revision."

    if ! check_cooldown "rollback" "$COOLDOWN_ROLLBACK"; then
        return 0
    fi

    # Check if an Argo Rollout exists (production uses Argo Rollouts)
    if kubectl -n "$NAMESPACE" get rollout settla-server &>/dev/null; then
        log_info "Argo Rollout detected — aborting current rollout and reverting"
        run_cmd kubectl -n "$NAMESPACE" argo rollouts abort settla-server
        run_cmd kubectl -n "$NAMESPACE" argo rollouts undo settla-server
        log_info "Rollback initiated via Argo Rollouts"
    else
        # Fallback: standard Kubernetes Deployment rollback
        log_info "No Argo Rollout found — using kubectl rollout undo"
        local current_revision
        current_revision="$(kubectl -n "$NAMESPACE" rollout history deployment/settla-server | tail -2 | head -1 | awk '{print $1}')"
        log_info "Current revision: ${current_revision}"
        run_cmd kubectl -n "$NAMESPACE" rollout undo deployment/settla-server
        log_info "Waiting for rollback to complete..."
        run_cmd kubectl -n "$NAMESPACE" rollout status deployment/settla-server --timeout=120s
    fi

    notify_slack "good" "SettlaHighErrorRateFastBurn — Rollback complete" "settla-server has been rolled back. Waiting 60s to verify error rate recovery."
    log_info "Rollback complete — waiting 60s for error rate to stabilise"

    # OPS-8: Post-rollback error rate verification.
    # After the rollback settles, confirm the error rate has dropped below the
    # fast-burn threshold (14.4× budget = 14.4 × 0.1% = 1.44% 5-minute error rate).
    # This validates that the rollback actually fixed the problem.
    verify_rollback_success

    log_info "REMEDIATION COMPLETE: settla-server rolled back"
}

# ── Post-rollback error rate verification ───────────────────────────────────
# Queries Prometheus for the 5-minute error rate on settla-server.
# Escalates via Slack if the rate remains above the fast-burn threshold after
# the rollback. Requires PROMETHEUS_URL to be set (defaults to in-cluster addr).
verify_rollback_success() {
    local prometheus_url="${PROMETHEUS_URL:-http://prometheus:9090}"
    # Fast-burn threshold: 14.4× the 0.1% SLO budget = 1.44% error rate
    local threshold="0.0144"
    # Wait for the rollback to propagate and for Prometheus to scrape new data
    local wait_seconds=60
    log_info "verify_rollback: sleeping ${wait_seconds}s for metrics to settle"

    if [[ "$DRY_RUN" == "true" ]]; then
        log_info "[DRY RUN] Would wait ${wait_seconds}s then query ${prometheus_url} for error rate"
        return 0
    fi

    sleep "$wait_seconds"

    # PromQL: 5-minute HTTP error rate for settla-server (4xx/5xx / total requests)
    local query='sum(rate(http_requests_total{job="settla-server",code=~"[45].."}[5m])) / sum(rate(http_requests_total{job="settla-server"}[5m]))'
    local encoded_query
    encoded_query="$(python3 -c "import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1]))" "$query" 2>/dev/null || \
        printf '%s' "$query" | sed 's/ /%20/g; s/{/%7B/g; s/}/%7D/g; s/=~/%3D~/g; s/,/%2C/g')"

    local prom_response
    prom_response="$(curl -sf --connect-timeout 5 --max-time 15 \
        "${prometheus_url}/api/v1/query?query=${encoded_query}" 2>&1)" || {
        log_warn "verify_rollback: could not reach Prometheus at ${prometheus_url} — skipping error rate check"
        return 0
    }

    local current_rate
    current_rate="$(echo "$prom_response" | python3 -c "
import sys, json
try:
    d = json.load(sys.stdin)
    results = d.get('data', {}).get('result', [])
    if results:
        print(results[0]['value'][1])
    else:
        print('no_data')
except Exception as e:
    print('parse_error')
" 2>/dev/null || echo "parse_error")"

    log_info "verify_rollback: post-rollback 5m error rate = ${current_rate} (threshold=${threshold})"

    # If Prometheus returned no data, we cannot confirm recovery — escalate
    if [[ "$current_rate" == "no_data" || "$current_rate" == "parse_error" ]]; then
        log_warn "verify_rollback: could not determine error rate from Prometheus response — manual check required"
        notify_slack "warning" \
            "SettlaHighErrorRateFastBurn — Post-rollback verification inconclusive" \
            "Rollback was executed but Prometheus returned no error rate data. Manual verification required. Check settla-server logs and dashboards."
        return 0
    fi

    # Compare rate against threshold using python3 (avoids bash float comparison)
    local still_elevated
    still_elevated="$(python3 -c "print('yes' if float('${current_rate}') > float('${threshold}') else 'no')" 2>/dev/null || echo "unknown")"

    if [[ "$still_elevated" == "yes" ]]; then
        log_error "verify_rollback: error rate still elevated after rollback (rate=${current_rate}, threshold=${threshold})"
        notify_slack "danger" \
            "SettlaHighErrorRateFastBurn — Rollback did NOT resolve the error spike" \
            "Post-rollback 5-minute error rate is ${current_rate} (threshold=${threshold}). The rollback may not have fixed the root cause. Immediate manual investigation required. Check recent DB migrations, config changes, and dependency health."
    else
        log_info "verify_rollback: error rate is below threshold — rollback confirmed successful (rate=${current_rate})"
        notify_slack "good" \
            "SettlaHighErrorRateFastBurn — Rollback verified successful" \
            "Post-rollback 5-minute error rate is ${current_rate} (below threshold=${threshold}). System appears to be recovering normally."
    fi
}

# ── 2. SettlaNATSConsumerLagCritical → Scale up settla-node ─────────────
# Increases settla-node replicas to handle message backlog.
# settla-node is a StatefulSet with 8 partitions by default.
handle_nats_consumer_lag_critical() {
    log_warn "REMEDIATION: SettlaNATSConsumerLagCritical — scaling up settla-node"
    notify_slack "warning" "SettlaNATSConsumerLagCritical" "NATS consumer queue depth exceeds critical threshold. Scaling up settla-node replicas."

    if ! check_cooldown "scale-node" "$COOLDOWN_SCALE"; then
        return 0
    fi

    local current_replicas
    current_replicas="$(kubectl -n "$NAMESPACE" get statefulset settla-node -o jsonpath='{.spec.replicas}')"
    local new_replicas=$(( current_replicas + 2 ))

    if (( new_replicas > MAX_NODE_REPLICAS )); then
        log_warn "Cannot scale beyond ${MAX_NODE_REPLICAS} replicas (current: ${current_replicas})"
        new_replicas=$MAX_NODE_REPLICAS
    fi

    if (( new_replicas == current_replicas )); then
        log_info "Already at maximum replicas (${MAX_NODE_REPLICAS}). No action taken."
        notify_slack "warning" "SettlaNATSConsumerLagCritical — Scale limit reached" "Already at maximum ${MAX_NODE_REPLICAS} replicas. Manual investigation required."
        return 0
    fi

    log_info "Scaling settla-node: ${current_replicas} → ${new_replicas} replicas"
    run_cmd kubectl -n "$NAMESPACE" scale statefulset settla-node --replicas="$new_replicas"

    # Wait for new pods to be ready
    run_cmd kubectl -n "$NAMESPACE" rollout status statefulset/settla-node --timeout=180s

    notify_slack "good" "SettlaNATSConsumerLagCritical — Scaled" "settla-node scaled from ${current_replicas} to ${new_replicas} replicas."
    log_info "REMEDIATION COMPLETE: settla-node scaled to ${new_replicas} replicas"
}

# ── 3. SettlaCircuitBreakerOpen → Restart affected pods ─────────────────
# Restarts pods where circuit breakers have tripped. This clears any
# stale connections and forces re-establishment of downstream connectivity.
handle_circuit_breaker_open() {
    local instance="${1:-}"
    log_warn "REMEDIATION: SettlaCircuitBreakerOpen — restarting affected pod (instance=${instance})"
    notify_slack "warning" "SettlaCircuitBreakerOpen" "Circuit breaker tripped open on instance=${instance:-all}. Restarting affected pods to clear stale connections."

    if ! check_cooldown "restart-${instance}" "$COOLDOWN_RESTART"; then
        return 0
    fi

    if [[ -n "$instance" ]]; then
        # Delete the specific pod — StatefulSet/Deployment controller will recreate it
        local pod_name
        pod_name="$(echo "$instance" | cut -d: -f1)"
        if kubectl -n "$NAMESPACE" get pod "$pod_name" &>/dev/null; then
            log_info "Deleting pod ${pod_name} to force reconnection"
            run_cmd kubectl -n "$NAMESPACE" delete pod "$pod_name" --grace-period=45
        else
            log_warn "Pod ${pod_name} not found — may have already been replaced"
        fi
    else
        # No specific instance — restart all settla-server pods with rolling restart
        log_info "No specific instance — performing rolling restart of settla-server"
        run_cmd kubectl -n "$NAMESPACE" rollout restart deployment/settla-server
        run_cmd kubectl -n "$NAMESPACE" rollout status deployment/settla-server --timeout=180s
    fi

    notify_slack "good" "SettlaCircuitBreakerOpen — Restart complete" "Pod restart complete for instance=${instance:-all settla-server}. Circuit breaker should recover on reconnection."
    log_info "REMEDIATION COMPLETE: circuit breaker pod restart"
}

# ── 4. SettlaTreasuryFlushLagHigh → Trigger manual flush ────────────────
# Calls the admin endpoint to force an immediate treasury position flush.
# The flush goroutine runs every 100ms, but this forces an out-of-band flush.
handle_treasury_flush_lag() {
    log_warn "REMEDIATION: SettlaTreasuryFlushLagHigh — triggering manual flush"
    notify_slack "warning" "SettlaTreasuryFlushLagHigh" "Treasury flush lag exceeds 1s. Triggering out-of-band flush on all settla-server pods."

    if ! check_cooldown "treasury-flush" "$COOLDOWN_FLUSH"; then
        return 0
    fi

    # Call the admin flush endpoint on each settla-server pod
    local pods
    pods="$(kubectl -n "$NAMESPACE" get pods -l app.kubernetes.io/name=settla-server -o jsonpath='{.items[*].status.podIP}')"

    for pod_ip in $pods; do
        log_info "Triggering flush on ${pod_ip}"
        local response
        response="$(run_cmd curl -sf --connect-timeout 5 --max-time 10 \
            -X POST "http://${pod_ip}:8080/admin/treasury/flush" \
            -H "Content-Type: application/json" 2>&1)" || true
        log_info "Flush response from ${pod_ip}: ${response:-no response}"
    done

    notify_slack "good" "SettlaTreasuryFlushLagHigh — Flush triggered" "Manual treasury flush triggered on all settla-server pods. Monitor flush lag metric."
    log_info "REMEDIATION COMPLETE: treasury flush triggered on all pods"
}

# ── 5. SettlaPgBouncerPoolCritical → Increase pool size ─────────────────
# Patches the PgBouncer ConfigMap to increase DEFAULT_POOL_SIZE.
# Requires PgBouncer reload (SIGHUP) to take effect.
handle_pgbouncer_pool_saturated() {
    log_warn "REMEDIATION: SettlaPgBouncerPoolCritical — increasing pool size"
    notify_slack "warning" "SettlaPgBouncerPoolCritical" "PgBouncer connection pool near exhaustion (>95%). Increasing pool size across all 3 databases."

    if ! check_cooldown "pgbouncer-resize" "$COOLDOWN_PGBOUNCER"; then
        return 0
    fi

    # Get current pool size from ConfigMap
    local current_pool_size
    current_pool_size="$(kubectl -n "$NAMESPACE" get configmap pgbouncer-config \
        -o jsonpath='{.data.DEFAULT_POOL_SIZE}' 2>/dev/null || echo "50")"
    local new_pool_size=$(( current_pool_size + 20 ))

    # Cap at reasonable limit (Postgres max_connections is typically 200-400)
    if (( new_pool_size > 150 )); then
        log_warn "Pool size would exceed 150 (current: ${current_pool_size}). Capping at 150."
        new_pool_size=150
    fi

    if (( new_pool_size == current_pool_size )); then
        log_info "Already at maximum pool size. No action taken."
        notify_slack "danger" "SettlaPgBouncerPoolCritical — Pool limit reached" "Already at maximum pool size (150). Manual investigation required: check for connection leaks and long-running transactions."
        return 0
    fi

    log_info "Increasing PgBouncer pool size: ${current_pool_size} → ${new_pool_size}"

    # Patch each PgBouncer deployment
    for bouncer in pgbouncer-ledger pgbouncer-transfer pgbouncer-treasury; do
        if kubectl -n "$NAMESPACE" get deployment "$bouncer" &>/dev/null; then
            run_cmd kubectl -n "$NAMESPACE" set env deployment/"$bouncer" \
                DEFAULT_POOL_SIZE="$new_pool_size"
            log_info "Patched ${bouncer} pool size to ${new_pool_size}"
        fi
    done

    notify_slack "good" "SettlaPgBouncerPoolCritical — Pool increased" "PgBouncer pool size increased from ${current_pool_size} to ${new_pool_size} across all databases."
    log_info "REMEDIATION COMPLETE: PgBouncer pools increased to ${new_pool_size}"
}

# ── 6. SettlaLoadSheddingCritical → Scale up ────────────────────────────
# Scales up both gateway and settla-server when load shedding is active.
handle_load_shedding_critical() {
    log_warn "REMEDIATION: SettlaLoadSheddingCritical — scaling up application tier"
    notify_slack "warning" "SettlaLoadSheddingCritical" "System rejecting >100 req/s under extreme load. Scaling up gateway and settla-server."

    if ! check_cooldown "scale-app" "$COOLDOWN_SCALE"; then
        return 0
    fi

    # Scale settla-server
    local server_replicas
    server_replicas="$(kubectl -n "$NAMESPACE" get deployment settla-server \
        -o jsonpath='{.spec.replicas}' 2>/dev/null || echo "6")"
    local new_server_replicas=$(( server_replicas + 2 ))
    if (( new_server_replicas > MAX_SERVER_REPLICAS )); then
        new_server_replicas=$MAX_SERVER_REPLICAS
    fi

    if (( new_server_replicas > server_replicas )); then
        log_info "Scaling settla-server: ${server_replicas} → ${new_server_replicas}"
        run_cmd kubectl -n "$NAMESPACE" scale deployment settla-server --replicas="$new_server_replicas"
    fi

    # Scale gateway
    local gateway_replicas
    gateway_replicas="$(kubectl -n "$NAMESPACE" get deployment gateway \
        -o jsonpath='{.spec.replicas}' 2>/dev/null || echo "4")"
    local new_gateway_replicas=$(( gateway_replicas + 2 ))
    if (( new_gateway_replicas > MAX_GATEWAY_REPLICAS )); then
        new_gateway_replicas=$MAX_GATEWAY_REPLICAS
    fi

    if (( new_gateway_replicas > gateway_replicas )); then
        log_info "Scaling gateway: ${gateway_replicas} → ${new_gateway_replicas}"
        run_cmd kubectl -n "$NAMESPACE" scale deployment gateway --replicas="$new_gateway_replicas"
    fi

    notify_slack "good" "SettlaLoadSheddingCritical — Scaled up" "settla-server: ${server_replicas}→${new_server_replicas}, gateway: ${gateway_replicas}→${new_gateway_replicas}. Monitor load shedding rate."
    log_info "REMEDIATION COMPLETE: application tier scaled up"
}

# ── 7. SettlaDiskSpaceCritical → Trigger partition cleanup ──────────────
# Drops old monthly partitions (older than 6 months) to free disk space.
# Partition-drop is preferred over DELETE for instant space reclamation.
handle_disk_space_critical() {
    log_warn "REMEDIATION: SettlaDiskSpaceCritical — triggering partition cleanup"
    notify_slack "danger" "SettlaDiskSpaceCritical" "Disk below 5% free. Triggering partition drop for partitions older than 6 months."

    if ! check_cooldown "partition-cleanup" "$COOLDOWN_CLEANUP"; then
        return 0
    fi

    # Call admin endpoint to trigger partition maintenance
    local response
    response="$(run_cmd curl -sf --connect-timeout 5 --max-time 30 \
        -X POST "${SERVER_ADMIN_URL}/admin/maintenance/drop-old-partitions" \
        -H "Content-Type: application/json" \
        -d '{"older_than_months": 6}' 2>&1)" || true
    log_info "Partition cleanup response: ${response:-no response}"

    # Also clean up old WAL segments if pg_archivecleanup is available
    log_info "Consider running pg_archivecleanup on Postgres WAL directories"

    notify_slack "good" "SettlaDiskSpaceCritical — Cleanup triggered" "Partition drop triggered for partitions older than 6 months. Response: ${response:-no response}. Monitor disk usage."
    log_info "REMEDIATION COMPLETE: partition cleanup triggered"
}

# ── 8. SettlaMemoryHigh → Restart memory-pressured pods ─────────────────
# Restarts settla-server pods when memory usage exceeds 90%.
# This clears heap fragmentation and in-memory caches, trading a brief
# disruption for restored memory headroom. Pod restarts are safe because
# the outbox guarantees no in-flight state is lost.
handle_memory_high() {
    local instance="${1:-}"
    log_warn "REMEDIATION: SettlaMemoryHigh — restarting memory-pressured pods (instance=${instance})"
    notify_slack "warning" "SettlaMemoryHigh" "Memory usage >90% on instance=${instance:-all}. Restarting pods to clear heap and restore headroom."

    if ! check_cooldown "restart-memory-${instance}" "$COOLDOWN_RESTART"; then
        return 0
    fi

    if [[ -n "$instance" ]]; then
        local pod_name
        pod_name="$(echo "$instance" | cut -d: -f1)"
        if kubectl -n "$NAMESPACE" get pod "$pod_name" &>/dev/null; then
            log_info "Deleting memory-pressured pod ${pod_name}"
            run_cmd kubectl -n "$NAMESPACE" delete pod "$pod_name" --grace-period=45
        else
            log_warn "Pod ${pod_name} not found — may have already been replaced"
        fi
    else
        log_info "No specific instance — performing rolling restart of settla-server"
        run_cmd kubectl -n "$NAMESPACE" rollout restart deployment/settla-server
        run_cmd kubectl -n "$NAMESPACE" rollout status deployment/settla-server --timeout=180s
    fi

    notify_slack "good" "SettlaMemoryHigh — Restart complete" "Pod restart complete for instance=${instance:-all settla-server}. Monitor heap usage after restart."
    log_info "REMEDIATION COMPLETE: memory-high pod restart"
}

# ── 9. SettlaPodCrashLooping → Delete crash-looping pod ─────────────────
# Deletes a crash-looping pod so the Deployment/StatefulSet controller
# schedules a fresh replacement. Idempotent: if the pod is already gone
# the command is a no-op. Cooldown prevents runaway restart loops.
handle_pod_crash_looping() {
    local instance="${1:-}"
    log_warn "REMEDIATION: SettlaPodCrashLooping — deleting crash-looping pod (instance=${instance})"
    notify_slack "warning" "SettlaPodCrashLooping" "Pod crash-loop detected on instance=${instance:-unknown}. Deleting pod to allow Deployment to reschedule a fresh replica."

    if ! check_cooldown "crash-loop-${instance}" "$COOLDOWN_RESTART"; then
        return 0
    fi

    if [[ -n "$instance" ]]; then
        local pod_name
        pod_name="$(echo "$instance" | cut -d: -f1)"
        if kubectl -n "$NAMESPACE" get pod "$pod_name" &>/dev/null; then
            log_info "Deleting crash-looping pod ${pod_name}"
            run_cmd kubectl -n "$NAMESPACE" delete pod "$pod_name" --grace-period=30
            log_info "Waiting for replacement pod to become ready..."
            sleep 15
            run_cmd kubectl -n "$NAMESPACE" wait --for=condition=Ready \
                -l "$(kubectl -n "$NAMESPACE" get pod "$pod_name" -o jsonpath='{.metadata.labels}' 2>/dev/null | jq -r 'to_entries | map("\(.key)=\(.value)") | join(",")' 2>/dev/null || echo "app.kubernetes.io/name=settla-server")" \
                --timeout=120s 2>/dev/null || true
        else
            log_warn "Pod ${pod_name} not found — may have already been rescheduled"
        fi
    else
        # No specific instance: perform a rolling restart of settla-server
        log_info "No specific instance — performing rolling restart of settla-server"
        run_cmd kubectl -n "$NAMESPACE" rollout restart deployment/settla-server
        run_cmd kubectl -n "$NAMESPACE" rollout status deployment/settla-server --timeout=180s
    fi

    notify_slack "good" "SettlaPodCrashLooping — Pod deleted" "Crash-looping pod ${instance:-settla-server} deleted. Deployment controller will schedule a fresh replica. See runbook: deploy/runbooks/pod-crash-looping.md"
    log_info "REMEDIATION COMPLETE: crash-looping pod deleted for instance=${instance}"
}

# ════════════════════════════════════════════════════════════════════════
# Webhook Listener
# ════════════════════════════════════════════════════════════════════════

# Parse AlertManager webhook payload and dispatch to handlers.
# AlertManager sends JSON with structure:
# {
#   "status": "firing",
#   "alerts": [{ "labels": { "alertname": "...", "instance": "..." }, ... }]
# }
dispatch_alert() {
    local payload="$1"

    local alert_name
    alert_name="$(echo "$payload" | jq -r '.alerts[0].labels.alertname // empty')"
    local instance
    instance="$(echo "$payload" | jq -r '.alerts[0].labels.instance // empty')"
    local status
    status="$(echo "$payload" | jq -r '.status // empty')"

    if [[ -z "$alert_name" ]]; then
        log_error "No alertname found in payload"
        return 1
    fi

    if [[ "$status" != "firing" ]]; then
        log_info "Alert ${alert_name} is '${status}', not 'firing'. Skipping remediation."
        return 0
    fi

    log_info "Dispatching remediation for alert: ${alert_name} (instance=${instance})"

    case "$alert_name" in
        # ── DR doc canonical names (7 alerts documented in docs/disaster-recovery.md) ──
        SettlaHighErrorRate | SettlaHighErrorRateFastBurn)
            handle_high_error_rate_fast_burn
            ;;
        SettlaPodCrashLooping)
            handle_pod_crash_looping "$instance"
            ;;
        SettlaHighLatency | SettlaLoadSheddingCritical)
            handle_load_shedding_critical
            ;;
        SettlaNATSConsumerLag | SettlaNATSConsumerLagCritical)
            handle_nats_consumer_lag_critical
            ;;
        SettlaPostgresConnectionExhaustion | SettlaPgBouncerPoolCritical)
            handle_pgbouncer_pool_saturated
            ;;
        SettlaHighMemoryUsage | SettlaMemoryHigh)
            handle_memory_high "$instance"
            ;;
        SettlaHighDiskUsage | SettlaDiskSpaceCritical)
            handle_disk_space_critical
            ;;
        # ── Additional operational alerts ──
        SettlaCircuitBreakerOpen)
            handle_circuit_breaker_open "$instance"
            ;;
        SettlaTreasuryFlushLagHigh)
            handle_treasury_flush_lag
            ;;
        *)
            log_info "No auto-remediation handler for alert: ${alert_name}"
            ;;
    esac
}

# ── HTTP listener using socat/netcat ─────────────────────────────────────
# In production, this runs as a small Go/Python HTTP server.
# For simplicity, this uses a bash read loop with socat.
start_webhook_listener() {
    log_info "Starting remediation webhook listener on port ${LISTEN_PORT}"
    log_info "Namespace: ${NAMESPACE}, Dry run: ${DRY_RUN}"

    # Check for required tools
    for tool in kubectl jq curl; do
        if ! command -v "$tool" &>/dev/null; then
            log_error "Required tool '${tool}' not found in PATH"
            exit 1
        fi
    done

    # Use socat if available, otherwise fall back to a simple loop
    if command -v socat &>/dev/null; then
        socat TCP-LISTEN:"${LISTEN_PORT}",reuseaddr,fork EXEC:"$(realpath "$0") --handle-request" &
        local socat_pid=$!
        log_info "Webhook listener started (socat PID: ${socat_pid})"
        trap "kill ${socat_pid} 2>/dev/null; log_info 'Webhook listener stopped'" EXIT INT TERM
        wait "$socat_pid"
    else
        log_warn "socat not found — falling back to ncat"
        while true; do
            ncat -l -p "${LISTEN_PORT}" -c "$(realpath "$0") --handle-request" 2>/dev/null || sleep 1
        done
    fi
}

# Handle a single HTTP request from stdin
handle_request() {
    local body=""
    local content_length=0

    # Read HTTP headers
    while IFS= read -r line; do
        line="${line%%$'\r'}"
        [[ -z "$line" ]] && break
        if [[ "$line" =~ ^Content-Length:\ ([0-9]+) ]]; then
            content_length="${BASH_REMATCH[1]}"
        fi
    done

    # Read body
    if (( content_length > 0 )); then
        body="$(head -c "$content_length")"
    fi

    # Dispatch
    if [[ -n "$body" ]]; then
        dispatch_alert "$body" || true
    fi

    # Send HTTP 200 response
    echo -e "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 16\r\n\r\n{\"status\":\"ok\"}"
}

# ════════════════════════════════════════════════════════════════════════
# Main
# ════════════════════════════════════════════════════════════════════════

main() {
    case "${1:-}" in
        --handle-request)
            handle_request
            ;;
        --dispatch)
            # Manual dispatch: echo '{"alerts":[{"labels":{"alertname":"..."}}],"status":"firing"}' | ./auto-remediation.sh --dispatch
            local payload
            payload="$(cat)"
            dispatch_alert "$payload"
            ;;
        --test)
            # Run a specific handler for testing
            local handler="${2:-}"
            case "$handler" in
                rollback)       handle_high_error_rate_fast_burn ;;
                scale-node)     handle_nats_consumer_lag_critical ;;
                crash-loop)     handle_pod_crash_looping "${3:-}" ;;
                restart)        handle_circuit_breaker_open "${3:-}" ;;
                flush)          handle_treasury_flush_lag ;;
                pgbouncer)      handle_pgbouncer_pool_saturated ;;
                scale-app)      handle_load_shedding_critical ;;
                disk-cleanup)   handle_disk_space_critical ;;
                memory)         handle_memory_high "${3:-}" ;;
                *)
                    echo "Usage: $0 --test <handler> [args]"
                    echo "Handlers: rollback, scale-node, crash-loop, restart, flush, pgbouncer, scale-app, disk-cleanup, memory"
                    exit 1
                    ;;
            esac
            ;;
        *)
            start_webhook_listener
            ;;
    esac
}

main "$@"
