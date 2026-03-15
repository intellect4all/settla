#!/usr/bin/env bash
# nats-backup.sh — Every-6-hour NATS JetStream stream snapshot to S3.
#
# Uses the official NATS CLI (`nats stream backup`) to create a point-in-time
# snapshot of all JetStream streams. The backup is a directory of binary
# message files; it is archived and uploaded to S3.
#
# Environment variables:
#   NATS_URL               — NATS server URL (default: nats://nats:4222)
#   NATS_CREDS             — path to NATS credentials file (optional)
#   S3_BUCKET              — target S3 bucket name (without s3:// prefix)
#   S3_PREFIX              — key prefix (default: nats)
#   ENVIRONMENT            — deployment environment
#   SLACK_WEBHOOK_URL      — Slack incoming-webhook URL
#   PAGERDUTY_ROUTING_KEY  — PagerDuty Events API v2 routing key
#   RETENTION_HOURS        — hours to retain snapshots (default: 48)
#
# Streams backed up (all 7 Settla JetStream streams — must match node/messaging/streams.go):
#   SETTLA_TRANSFERS         — transfer event stream (8 partitions by tenant hash)
#   SETTLA_PROVIDERS         — provider command stream (on-ramp, off-ramp)
#   SETTLA_LEDGER            — ledger post/reverse commands
#   SETTLA_TREASURY          — treasury reserve/release commands
#   SETTLA_BLOCKCHAIN        — blockchain send/confirm commands
#   SETTLA_WEBHOOKS          — outbound tenant webhook delivery queue
#   SETTLA_PROVIDER_WEBHOOKS — inbound async provider callback stream
#
# In production these streams have R=3 replication, so the cluster remains
# healthy during backup of any single node.

set -euo pipefail

NATS_URL="${NATS_URL:-nats://nats:4222}"
S3_PREFIX="${S3_PREFIX:-nats}"
ENVIRONMENT="${ENVIRONMENT:-production}"
RETENTION_HOURS="${RETENTION_HOURS:-48}"
TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"
BACKUP_DIR="/tmp/nats-backups/${TIMESTAMP}"
EXIT_CODE=0

# All 7 Settla JetStream streams — must stay in sync with node/messaging/streams.go
STREAMS=(
  "SETTLA_TRANSFERS"
  "SETTLA_PROVIDERS"
  "SETTLA_LEDGER"
  "SETTLA_TREASURY"
  "SETTLA_BLOCKCHAIN"
  "SETTLA_WEBHOOKS"
  "SETTLA_PROVIDER_WEBHOOKS"
)

log() { echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] $*"; }

notify_failure() {
  local subject="$1"
  local detail="$2"
  log "ERROR: ${subject} — ${detail}"

  if [[ -n "${SLACK_WEBHOOK_URL:-}" ]]; then
    curl -s -X POST "${SLACK_WEBHOOK_URL}" \
      -H 'Content-Type: application/json' \
      -d "$(cat <<EOF
{
  "text": ":rotating_light: *[${ENVIRONMENT}] NATS Backup Failed*",
  "attachments": [
    {
      "color": "danger",
      "fields": [
        {"title": "Subject",     "value": "${subject}",     "short": false},
        {"title": "Detail",      "value": "${detail}",      "short": false},
        {"title": "Timestamp",   "value": "${TIMESTAMP}",   "short": true},
        {"title": "Environment", "value": "${ENVIRONMENT}", "short": true}
      ],
      "footer": "Settla Backup Automation"
    }
  ]
}
EOF
)" || log "WARNING: Slack notification failed"
  fi

  if [[ -n "${PAGERDUTY_ROUTING_KEY:-}" ]]; then
    curl -s -X POST "https://events.pagerduty.com/v2/enqueue" \
      -H 'Content-Type: application/json' \
      -d "$(cat <<EOF
{
  "routing_key": "${PAGERDUTY_ROUTING_KEY}",
  "event_action": "trigger",
  "payload": {
    "summary": "[${ENVIRONMENT}] NATS Backup Failed: ${subject}",
    "severity": "critical",
    "source": "settla-nats-backup-cronjob",
    "custom_details": {
      "detail": "${detail}",
      "timestamp": "${TIMESTAMP}",
      "environment": "${ENVIRONMENT}"
    }
  },
  "dedup_key": "settla-nats-backup-$(date -u +%Y%m%dT%H)"
}
EOF
)" || log "WARNING: PagerDuty alert failed"
  fi
}

notify_success() {
  log "SUCCESS: $1"
  if [[ -n "${SLACK_WEBHOOK_URL:-}" ]]; then
    curl -s -X POST "${SLACK_WEBHOOK_URL}" \
      -H 'Content-Type: application/json' \
      -d "$(cat <<EOF
{
  "text": ":white_check_mark: *[${ENVIRONMENT}] NATS Backup Completed*",
  "attachments": [
    {
      "color": "good",
      "fields": [
        {"title": "Detail",      "value": "$1",             "short": false},
        {"title": "Timestamp",   "value": "${TIMESTAMP}",   "short": true},
        {"title": "Environment", "value": "${ENVIRONMENT}", "short": true}
      ],
      "footer": "Settla Backup Automation"
    }
  ]
}
EOF
)" || true
  fi
}

# ---------------------------------------------------------------------------
# Pre-flight checks
# ---------------------------------------------------------------------------
for bin in nats aws tar gzip; do
  if ! command -v "${bin}" &>/dev/null; then
    notify_failure "Missing binary: ${bin}" "Install nats-cli, aws-cli, tar, and gzip in the backup image."
    exit 1
  fi
done

mkdir -p "${BACKUP_DIR}"

# Build NATS CLI auth flags
NATS_AUTH_FLAGS=("--server=${NATS_URL}")
if [[ -n "${NATS_CREDS:-}" && -f "${NATS_CREDS}" ]]; then
  NATS_AUTH_FLAGS+=("--creds=${NATS_CREDS}")
fi

# ---------------------------------------------------------------------------
# Verify NATS connectivity
# ---------------------------------------------------------------------------
log "Verifying NATS connectivity at ${NATS_URL} ..."
if ! nats "${NATS_AUTH_FLAGS[@]}" server ping --count=1 2>/dev/null; then
  notify_failure "NATS connectivity check failed" "Cannot reach ${NATS_URL}"
  exit 1
fi
log "NATS connectivity OK"

# ---------------------------------------------------------------------------
# Snapshot each stream
# ---------------------------------------------------------------------------
SUCCESSFUL_STREAMS=()
FAILED_STREAMS=()

for stream in "${STREAMS[@]}"; do
  stream_dir="${BACKUP_DIR}/${stream}"
  mkdir -p "${stream_dir}"

  log "Snapshotting stream: ${stream} -> ${stream_dir}"

  # nats stream backup creates a directory of message files
  if nats "${NATS_AUTH_FLAGS[@]}" stream backup \
      "${stream}" \
      "${stream_dir}" \
      2>"/tmp/nats-${stream}.err"; then
    MSG_COUNT="$(find "${stream_dir}" -type f | wc -l)"
    log "Stream ${stream}: ${MSG_COUNT} files captured"
    SUCCESSFUL_STREAMS+=("${stream} (${MSG_COUNT} files)")
  else
    ERR="$(cat /tmp/nats-${stream}.err)"
    # If the stream does not exist yet (e.g., fresh deployment), warn but don't fail
    if echo "${ERR}" | grep -q "not found\|does not exist"; then
      log "WARNING: Stream ${stream} not found — skipping"
    else
      notify_failure "Stream snapshot failed: ${stream}" "${ERR}"
      FAILED_STREAMS+=("${stream}")
      EXIT_CODE=1
    fi
  fi
done

# ---------------------------------------------------------------------------
# Archive and compress all stream snapshots together
# ---------------------------------------------------------------------------
ARCHIVE="${BACKUP_DIR}.tar.gz"
log "Creating archive: ${ARCHIVE}"
tar -czf "${ARCHIVE}" -C "$(dirname "${BACKUP_DIR}")" "$(basename "${BACKUP_DIR}")"
ARCHIVE_SIZE="$(du -sh "${ARCHIVE}" | cut -f1)"
log "Archive created: ${ARCHIVE} (${ARCHIVE_SIZE})"

# ---------------------------------------------------------------------------
# Upload to S3
# ---------------------------------------------------------------------------
S3_KEY="${S3_PREFIX}/${ENVIRONMENT}/$(date -u +%Y/%m/%d)/nats-streams-${TIMESTAMP}.tar.gz"
log "Uploading to s3://${S3_BUCKET}/${S3_KEY} ..."

if aws s3 cp "${ARCHIVE}" "s3://${S3_BUCKET}/${S3_KEY}" \
    --storage-class STANDARD_IA; then
  log "Upload successful (${ARCHIVE_SIZE})"
  rm -rf "${BACKUP_DIR}" "${ARCHIVE}"
else
  notify_failure "S3 upload failed" "Could not upload ${ARCHIVE} to s3://${S3_BUCKET}/${S3_KEY}"
  EXIT_CODE=1
fi

# ---------------------------------------------------------------------------
# Retention enforcement — delete snapshots older than RETENTION_HOURS
# ---------------------------------------------------------------------------
log "Enforcing ${RETENTION_HOURS}h retention on s3://${S3_BUCKET}/${S3_PREFIX}/${ENVIRONMENT}/ ..."

CUTOFF="$(date -u -d "${RETENTION_HOURS} hours ago" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null \
          || date -u -v-"${RETENTION_HOURS}"H +%Y-%m-%dT%H:%M:%SZ)"

aws s3api list-objects-v2 \
  --bucket "${S3_BUCKET}" \
  --prefix "${S3_PREFIX}/${ENVIRONMENT}/" \
  --query "Contents[?LastModified<='${CUTOFF}'].Key" \
  --output text 2>/dev/null \
| tr '\t' '\n' \
| while read -r key; do
    [[ -z "${key}" || "${key}" == "None" ]] && continue
    log "Deleting expired snapshot: ${key}"
    aws s3 rm "s3://${S3_BUCKET}/${key}" || log "WARNING: Failed to delete ${key}"
  done

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
if [[ ${#SUCCESSFUL_STREAMS[@]} -gt 0 ]]; then
  SUMMARY="Streams: $(IFS=', '; echo "${SUCCESSFUL_STREAMS[*]}")"
  if [[ "${EXIT_CODE}" -eq 0 ]]; then
    notify_success "${SUMMARY} -> s3://${S3_BUCKET}/${S3_KEY} (${ARCHIVE_SIZE})"
  fi
fi

if [[ ${#FAILED_STREAMS[@]} -gt 0 ]]; then
  notify_failure "${#FAILED_STREAMS[@]} stream(s) failed to snapshot" \
    "$(IFS=', '; echo "${FAILED_STREAMS[*]}")"
fi

exit "${EXIT_CODE}"
