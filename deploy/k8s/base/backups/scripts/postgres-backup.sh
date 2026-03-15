#!/usr/bin/env bash
# postgres-backup.sh — Full pg_dump for all three Settla PostgreSQL databases.
#
# Environment variables (injected via Secret/ConfigMap):
#   POSTGRES_PASSWORD      — shared password for the settla user
#   S3_BUCKET              — target S3 bucket (e.g. s3://settla-backups-prod)
#   S3_PREFIX              — key prefix (default: postgres)
#   ENVIRONMENT            — deployment environment (production, staging, development)
#   SLACK_WEBHOOK_URL      — Slack incoming-webhook URL for failure notifications
#   PAGERDUTY_ROUTING_KEY  — PagerDuty Events API v2 routing key (optional; leave blank for non-prod)
#   RETENTION_DAYS         — number of days to retain backups (default: 30)
#
# Required AWS credentials are provided via IRSA (IAM Roles for Service Accounts).
# The Pod's ServiceAccount annotation eks.amazonaws.com/role-arn triggers automatic
# token mounting at /var/run/secrets/eks.amazonaws.com/serviceaccount/token.

set -euo pipefail

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
S3_PREFIX="${S3_PREFIX:-postgres}"
ENVIRONMENT="${ENVIRONMENT:-production}"
RETENTION_DAYS="${RETENTION_DAYS:-30}"
TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"
DATE="$(date -u +%Y-%m-%d)"
BACKUP_DIR="/tmp/pg-backups"
EXIT_CODE=0

DATABASES=(
  "settla_ledger:postgres-ledger:5432"
  "settla_transfer:postgres-transfer:5432"
  "settla_treasury:postgres-treasury:5432"
)

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
log() { echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] $*"; }

notify_failure() {
  local subject="$1"
  local detail="$2"

  log "ERROR: ${subject}"

  # Slack notification
  if [[ -n "${SLACK_WEBHOOK_URL:-}" ]]; then
    curl -s -X POST "${SLACK_WEBHOOK_URL}" \
      -H 'Content-Type: application/json' \
      -d "$(cat <<EOF
{
  "text": ":rotating_light: *[${ENVIRONMENT}] PostgreSQL Backup Failed*",
  "attachments": [
    {
      "color": "danger",
      "fields": [
        {"title": "Subject", "value": "${subject}", "short": false},
        {"title": "Detail", "value": "${detail}", "short": false},
        {"title": "Timestamp", "value": "${TIMESTAMP}", "short": true},
        {"title": "Environment", "value": "${ENVIRONMENT}", "short": true}
      ],
      "footer": "Settla Backup Automation"
    }
  ]
}
EOF
)" || log "WARNING: Failed to send Slack notification"
  fi

  # PagerDuty notification (production only)
  if [[ -n "${PAGERDUTY_ROUTING_KEY:-}" ]]; then
    curl -s -X POST "https://events.pagerduty.com/v2/enqueue" \
      -H 'Content-Type: application/json' \
      -d "$(cat <<EOF
{
  "routing_key": "${PAGERDUTY_ROUTING_KEY}",
  "event_action": "trigger",
  "payload": {
    "summary": "[${ENVIRONMENT}] PostgreSQL Backup Failed: ${subject}",
    "severity": "critical",
    "source": "settla-backup-cronjob",
    "custom_details": {
      "detail": "${detail}",
      "timestamp": "${TIMESTAMP}",
      "environment": "${ENVIRONMENT}"
    }
  },
  "dedup_key": "settla-pg-backup-${DATE}"
}
EOF
)" || log "WARNING: Failed to send PagerDuty alert"
  fi
}

notify_success() {
  local message="$1"
  log "SUCCESS: ${message}"

  if [[ -n "${SLACK_WEBHOOK_URL:-}" ]]; then
    curl -s -X POST "${SLACK_WEBHOOK_URL}" \
      -H 'Content-Type: application/json' \
      -d "$(cat <<EOF
{
  "text": ":white_check_mark: *[${ENVIRONMENT}] PostgreSQL Backup Completed*",
  "attachments": [
    {
      "color": "good",
      "fields": [
        {"title": "Detail", "value": "${message}", "short": false},
        {"title": "Timestamp", "value": "${TIMESTAMP}", "short": true},
        {"title": "Environment", "value": "${ENVIRONMENT}", "short": true}
      ],
      "footer": "Settla Backup Automation"
    }
  ]
}
EOF
)" || log "WARNING: Failed to send Slack success notification"
  fi
}

# ---------------------------------------------------------------------------
# Pre-flight checks
# ---------------------------------------------------------------------------
for bin in pg_dump aws gzip; do
  if ! command -v "${bin}" &>/dev/null; then
    notify_failure "Missing required binary: ${bin}" "Install ${bin} in the backup container image."
    exit 1
  fi
done

mkdir -p "${BACKUP_DIR}"

# ---------------------------------------------------------------------------
# Backup each database
# ---------------------------------------------------------------------------
SUCCESSFUL_BACKUPS=()
FAILED_BACKUPS=()

for entry in "${DATABASES[@]}"; do
  IFS=':' read -r dbname host port <<< "${entry}"
  dump_file="${BACKUP_DIR}/${dbname}_${TIMESTAMP}.pgdump.gz"
  s3_key="${S3_PREFIX}/${ENVIRONMENT}/${DATE}/${dbname}_${TIMESTAMP}.pgdump.gz"

  log "Backing up ${dbname} from ${host}:${port} ..."

  if PGPASSWORD="${POSTGRES_PASSWORD}" pg_dump \
      --host="${host}" \
      --port="${port}" \
      --username="settla" \
      --dbname="${dbname}" \
      --format=custom \
      --compress=0 \
      --no-password \
      2>"/tmp/${dbname}-dump.err" | gzip > "${dump_file}"; then

    FILE_SIZE="$(du -sh "${dump_file}" | cut -f1)"
    log "Dump complete: ${dump_file} (${FILE_SIZE})"

    if aws s3 cp "${dump_file}" "s3://${S3_BUCKET}/${s3_key}" \
        --storage-class STANDARD_IA \
        2>"/tmp/${dbname}-s3.err"; then
      log "Uploaded: s3://${S3_BUCKET}/${s3_key} (${FILE_SIZE})"
      SUCCESSFUL_BACKUPS+=("${dbname} -> ${s3_key} (${FILE_SIZE})")
      rm -f "${dump_file}"
    else
      ERR="$(cat /tmp/${dbname}-s3.err)"
      notify_failure "S3 upload failed for ${dbname}" "${ERR}"
      FAILED_BACKUPS+=("${dbname}: S3 upload error")
      EXIT_CODE=1
    fi
  else
    ERR="$(cat /tmp/${dbname}-dump.err)"
    notify_failure "pg_dump failed for ${dbname}" "${ERR}"
    FAILED_BACKUPS+=("${dbname}: pg_dump error")
    EXIT_CODE=1
  fi
done

# ---------------------------------------------------------------------------
# Retention enforcement — delete objects older than RETENTION_DAYS
# ---------------------------------------------------------------------------
log "Enforcing ${RETENTION_DAYS}-day retention on s3://${S3_BUCKET}/${S3_PREFIX}/${ENVIRONMENT}/ ..."

CUTOFF="$(date -u -d "${RETENTION_DAYS} days ago" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null \
          || date -u -v-"${RETENTION_DAYS}"d +%Y-%m-%dT%H:%M:%SZ)"  # macOS fallback

aws s3api list-objects-v2 \
  --bucket "${S3_BUCKET#s3://}" \
  --prefix "${S3_PREFIX}/${ENVIRONMENT}/" \
  --query "Contents[?LastModified<='${CUTOFF}'].Key" \
  --output text 2>/dev/null \
| tr '\t' '\n' \
| while read -r key; do
    [[ -z "${key}" || "${key}" == "None" ]] && continue
    log "Deleting expired backup: ${key}"
    aws s3 rm "s3://${S3_BUCKET#s3://}/${key}" || log "WARNING: Failed to delete ${key}"
  done

# ---------------------------------------------------------------------------
# Summary report
# ---------------------------------------------------------------------------
if [[ ${#SUCCESSFUL_BACKUPS[@]} -gt 0 ]]; then
  SUMMARY="$(printf '%s\n' "${SUCCESSFUL_BACKUPS[@]}")"
  notify_success "All ${#SUCCESSFUL_BACKUPS[@]} database(s) backed up successfully.\n${SUMMARY}"
fi

if [[ ${#FAILED_BACKUPS[@]} -gt 0 ]]; then
  SUMMARY="$(printf '%s\n' "${FAILED_BACKUPS[@]}")"
  notify_failure "${#FAILED_BACKUPS[@]} backup(s) failed" "${SUMMARY}"
fi

exit "${EXIT_CODE}"
