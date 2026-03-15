#!/usr/bin/env bash
# tigerbeetle-backup.sh — Hourly snapshot of TigerBeetle data file to S3.
#
# TigerBeetle does not have a built-in backup command; the canonical approach
# is to copy the data file while the node is quiesced or to use a filesystem
# snapshot. This script uses an alternate strategy: it runs as a sidecar with
# the shared TigerBeetle data volume, copies the data file, then uploads to S3.
#
# For production 3-node clusters this script targets ONE replica at a time
# (replica-0 by default) so the cluster remains fully available during backup.
#
# Environment variables:
#   TB_DATA_FILE           — path to data file (default: /data/0_0.tigerbeetle)
#   TB_REPLICA_INDEX       — replica index to back up (default: 0)
#   S3_BUCKET              — target S3 bucket name (without s3:// prefix)
#   S3_PREFIX              — key prefix (default: tigerbeetle)
#   ENVIRONMENT            — deployment environment
#   SLACK_WEBHOOK_URL      — Slack incoming-webhook URL
#   PAGERDUTY_ROUTING_KEY  — PagerDuty Events API v2 routing key
#   RETENTION_HOURS        — hours to retain snapshots (default: 72)

set -euo pipefail

TB_DATA_FILE="${TB_DATA_FILE:-/data/0_0.tigerbeetle}"
TB_REPLICA_INDEX="${TB_REPLICA_INDEX:-0}"
S3_PREFIX="${S3_PREFIX:-tigerbeetle}"
ENVIRONMENT="${ENVIRONMENT:-production}"
RETENTION_HOURS="${RETENTION_HOURS:-72}"
TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"
BACKUP_DIR="/tmp/tb-backups"
EXIT_CODE=0

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
  "text": ":rotating_light: *[${ENVIRONMENT}] TigerBeetle Backup Failed*",
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
    "summary": "[${ENVIRONMENT}] TigerBeetle Backup Failed: ${subject}",
    "severity": "critical",
    "source": "settla-tigerbeetle-backup-cronjob",
    "custom_details": {
      "detail": "${detail}",
      "timestamp": "${TIMESTAMP}",
      "environment": "${ENVIRONMENT}"
    }
  },
  "dedup_key": "settla-tb-backup-$(date -u +%Y%m%dT%H)"
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
  "text": ":white_check_mark: *[${ENVIRONMENT}] TigerBeetle Backup Completed*",
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
for bin in aws gzip; do
  if ! command -v "${bin}" &>/dev/null; then
    notify_failure "Missing binary: ${bin}" "Ensure aws-cli and gzip are installed in the backup image."
    exit 1
  fi
done

if [[ ! -f "${TB_DATA_FILE}" ]]; then
  notify_failure "TigerBeetle data file not found" "${TB_DATA_FILE} does not exist"
  exit 1
fi

mkdir -p "${BACKUP_DIR}"

# ---------------------------------------------------------------------------
# Consistent copy:
# Use cp --reflink=auto (copy-on-write) when the filesystem supports it (btrfs,
# xfs); otherwise fall back to a regular copy. TigerBeetle's data file is
# append-only and uses a write-ahead log, so a point-in-time copy of a clean
# (non-mid-write) file is a valid, recoverable snapshot.
# For production, schedule this against the non-leader (Raft follower) replica
# to minimise contention with live writes.
# ---------------------------------------------------------------------------
SNAPSHOT_FILE="${BACKUP_DIR}/tigerbeetle-replica${TB_REPLICA_INDEX}-${TIMESTAMP}.data"

log "Copying TigerBeetle data file: ${TB_DATA_FILE} -> ${SNAPSHOT_FILE}"
if ! cp --reflink=auto "${TB_DATA_FILE}" "${SNAPSHOT_FILE}" 2>/dev/null; then
  cp "${TB_DATA_FILE}" "${SNAPSHOT_FILE}"
fi

FILE_SIZE="$(du -sh "${SNAPSHOT_FILE}" | cut -f1)"
log "Snapshot created: ${SNAPSHOT_FILE} (${FILE_SIZE})"

log "Compressing snapshot ..."
gzip "${SNAPSHOT_FILE}"
SNAPSHOT_FILE="${SNAPSHOT_FILE}.gz"
COMPRESSED_SIZE="$(du -sh "${SNAPSHOT_FILE}" | cut -f1)"
log "Compressed: ${SNAPSHOT_FILE} (${COMPRESSED_SIZE})"

# ---------------------------------------------------------------------------
# Upload to S3
# ---------------------------------------------------------------------------
S3_KEY="${S3_PREFIX}/${ENVIRONMENT}/$(date -u +%Y/%m/%d/%H)/tigerbeetle-replica${TB_REPLICA_INDEX}-${TIMESTAMP}.data.gz"

log "Uploading to s3://${S3_BUCKET}/${S3_KEY} ..."
if aws s3 cp "${SNAPSHOT_FILE}" "s3://${S3_BUCKET}/${S3_KEY}" \
    --storage-class STANDARD_IA; then
  log "Upload successful (${COMPRESSED_SIZE})"
  rm -f "${SNAPSHOT_FILE}"
else
  notify_failure "S3 upload failed" "Could not upload ${SNAPSHOT_FILE} to s3://${S3_BUCKET}/${S3_KEY}"
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

if [[ "${EXIT_CODE}" -eq 0 ]]; then
  notify_success "TigerBeetle replica-${TB_REPLICA_INDEX} -> s3://${S3_BUCKET}/${S3_KEY} (${COMPRESSED_SIZE})"
fi

exit "${EXIT_CODE}"
