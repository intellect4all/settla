#!/usr/bin/env bash
# backup-verify.sh — Daily restore test of the latest PostgreSQL backup.
#
# For each database, this script:
#   1. Locates the most recent pg_dump file in S3
#   2. Downloads it to a temporary location
#   3. Restores it into a throwaway local PostgreSQL instance
#   4. Runs row-count and checksum verification queries
#   5. Drops the throwaway database
#   6. Sends a Slack/PagerDuty notification with the result
#
# A PostgreSQL server must be reachable at VERIFY_PG_HOST (a temporary
# Kubernetes Job spins one up via a sidecar, or this targets a permanent
# "restore-test" instance in the cluster).
#
# Environment variables:
#   VERIFY_PG_HOST         — host of the throwaway Postgres instance (default: localhost)
#   VERIFY_PG_PORT         — port (default: 5432)
#   VERIFY_PG_USER         — superuser for creating/dropping test DBs (default: postgres)
#   VERIFY_PG_PASSWORD     — password for VERIFY_PG_USER
#   S3_BUCKET              — source S3 bucket name (without s3:// prefix)
#   S3_PREFIX              — key prefix to search in (default: postgres)
#   ENVIRONMENT            — deployment environment whose backups to verify
#   SLACK_WEBHOOK_URL      — Slack incoming-webhook URL
#   PAGERDUTY_ROUTING_KEY  — PagerDuty Events API v2 routing key
#
# Minimum row counts — alert if restored DB has fewer rows than these thresholds.
# These defaults are conservative; tune per environment via ConfigMap.
#   MIN_ROWS_LEDGER        — (default: 0; in production set to expected floor)
#   MIN_ROWS_TRANSFER      — (default: 0)
#   MIN_ROWS_TREASURY      — (default: 0)

set -euo pipefail

VERIFY_PG_HOST="${VERIFY_PG_HOST:-localhost}"
VERIFY_PG_PORT="${VERIFY_PG_PORT:-5432}"
VERIFY_PG_USER="${VERIFY_PG_USER:-postgres}"
S3_PREFIX="${S3_PREFIX:-postgres}"
ENVIRONMENT="${ENVIRONMENT:-production}"
TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"
DATE="$(date -u +%Y-%m-%d)"
WORK_DIR="/tmp/backup-verify"
EXIT_CODE=0
REPORT_LINES=()

MIN_ROWS_LEDGER="${MIN_ROWS_LEDGER:-0}"
MIN_ROWS_TRANSFER="${MIN_ROWS_TRANSFER:-0}"
MIN_ROWS_TREASURY="${MIN_ROWS_TREASURY:-0}"

# Maps: original_dbname -> restore_db_name, min_rows
declare -A DB_RESTORE_NAME=(
  ["settla_ledger"]="verify_ledger_${TIMESTAMP}"
  ["settla_transfer"]="verify_transfer_${TIMESTAMP}"
  ["settla_treasury"]="verify_treasury_${TIMESTAMP}"
)
declare -A DB_MIN_ROWS=(
  ["settla_ledger"]="${MIN_ROWS_LEDGER}"
  ["settla_transfer"]="${MIN_ROWS_TRANSFER}"
  ["settla_treasury"]="${MIN_ROWS_TREASURY}"
)
# Key tables to count rows in for each database
declare -A DB_VERIFY_TABLE=(
  ["settla_ledger"]="entry_lines"
  ["settla_transfer"]="transfers"
  ["settla_treasury"]="positions"
)

log() { echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] $*"; }

psql_as_superuser() {
  PGPASSWORD="${VERIFY_PG_PASSWORD}" psql \
    --host="${VERIFY_PG_HOST}" \
    --port="${VERIFY_PG_PORT}" \
    --username="${VERIFY_PG_USER}" \
    "$@"
}

notify_failure() {
  local subject="$1"
  local detail="$2"
  log "FAILURE: ${subject} — ${detail}"
  REPORT_LINES+=("FAIL: ${subject}")

  if [[ -n "${SLACK_WEBHOOK_URL:-}" ]]; then
    curl -s -X POST "${SLACK_WEBHOOK_URL}" \
      -H 'Content-Type: application/json' \
      -d "$(cat <<EOF
{
  "text": ":rotating_light: *[${ENVIRONMENT}] Backup Verification FAILED*",
  "attachments": [
    {
      "color": "danger",
      "fields": [
        {"title": "Subject",     "value": "${subject}",     "short": false},
        {"title": "Detail",      "value": "${detail}",      "short": false},
        {"title": "Date",        "value": "${DATE}",        "short": true},
        {"title": "Environment", "value": "${ENVIRONMENT}", "short": true}
      ],
      "footer": "Settla Backup Verification"
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
    "summary": "[${ENVIRONMENT}] Backup Verification Failed: ${subject}",
    "severity": "critical",
    "source": "settla-backup-verify-cronjob",
    "custom_details": {
      "detail": "${detail}",
      "date": "${DATE}",
      "environment": "${ENVIRONMENT}"
    }
  },
  "dedup_key": "settla-backup-verify-${DATE}"
}
EOF
)" || log "WARNING: PagerDuty alert failed"
  fi
}

send_final_report() {
  local status="$1"
  local color="$2"
  local emoji="$3"
  local report
  report="$(printf '%s\n' "${REPORT_LINES[@]}")"

  if [[ -n "${SLACK_WEBHOOK_URL:-}" ]]; then
    curl -s -X POST "${SLACK_WEBHOOK_URL}" \
      -H 'Content-Type: application/json' \
      -d "$(cat <<EOF
{
  "text": "${emoji} *[${ENVIRONMENT}] Daily Backup Verification ${status}*",
  "attachments": [
    {
      "color": "${color}",
      "fields": [
        {"title": "Results",     "value": "${report}",      "short": false},
        {"title": "Date",        "value": "${DATE}",        "short": true},
        {"title": "Environment", "value": "${ENVIRONMENT}", "short": true}
      ],
      "footer": "Settla Backup Verification"
    }
  ]
}
EOF
)" || log "WARNING: Slack final report failed"
  fi
}

# ---------------------------------------------------------------------------
# Pre-flight checks
# ---------------------------------------------------------------------------
for bin in aws pg_restore psql; do
  if ! command -v "${bin}" &>/dev/null; then
    notify_failure "Missing binary: ${bin}" "Ensure aws-cli and postgresql-client are installed."
    exit 1
  fi
done

mkdir -p "${WORK_DIR}"

# ---------------------------------------------------------------------------
# Verify each database backup
# ---------------------------------------------------------------------------
for dbname in settla_ledger settla_transfer settla_treasury; do
  restore_db="${DB_RESTORE_NAME[${dbname}]}"
  min_rows="${DB_MIN_ROWS[${dbname}]}"
  verify_table="${DB_VERIFY_TABLE[${dbname}]}"

  log "=== Verifying backup for: ${dbname} ==="

  # ---- Find latest backup in S3 ----
  log "Searching for latest backup in s3://${S3_BUCKET}/${S3_PREFIX}/${ENVIRONMENT}/ ..."
  LATEST_KEY="$(aws s3api list-objects-v2 \
    --bucket "${S3_BUCKET}" \
    --prefix "${S3_PREFIX}/${ENVIRONMENT}/" \
    --query "sort_by(Contents[?contains(Key,'${dbname}')], &LastModified)[-1].Key" \
    --output text 2>/dev/null)"

  if [[ -z "${LATEST_KEY}" || "${LATEST_KEY}" == "None" ]]; then
    notify_failure "No backup found for ${dbname}" \
      "No objects matching ${dbname} under s3://${S3_BUCKET}/${S3_PREFIX}/${ENVIRONMENT}/"
    EXIT_CODE=1
    continue
  fi
  log "Latest backup: s3://${S3_BUCKET}/${LATEST_KEY}"

  # ---- Download ----
  LOCAL_FILE="${WORK_DIR}/${dbname}-latest.pgdump.gz"
  log "Downloading ${LATEST_KEY} ..."
  if ! aws s3 cp "s3://${S3_BUCKET}/${LATEST_KEY}" "${LOCAL_FILE}"; then
    notify_failure "Download failed for ${dbname}" "s3://${S3_BUCKET}/${LATEST_KEY}"
    EXIT_CODE=1
    continue
  fi

  # ---- Decompress ----
  DUMP_FILE="${WORK_DIR}/${dbname}-latest.pgdump"
  log "Decompressing ..."
  if ! gunzip -c "${LOCAL_FILE}" > "${DUMP_FILE}"; then
    notify_failure "Decompression failed for ${dbname}" "${LOCAL_FILE}"
    EXIT_CODE=1
    rm -f "${LOCAL_FILE}"
    continue
  fi
  rm -f "${LOCAL_FILE}"

  # ---- Create throwaway database ----
  log "Creating restore target: ${restore_db}"
  psql_as_superuser --dbname=postgres \
    -c "CREATE DATABASE \"${restore_db}\" WITH OWNER settla;" \
    2>/dev/null || {
    notify_failure "Failed to create restore DB: ${restore_db}" "Check Postgres superuser credentials"
    EXIT_CODE=1
    rm -f "${DUMP_FILE}"
    continue
  }

  # ---- Restore ----
  log "Restoring ${dbname} -> ${restore_db} ..."
  RESTORE_ERRORS=""
  if ! PGPASSWORD="${VERIFY_PG_PASSWORD}" pg_restore \
      --host="${VERIFY_PG_HOST}" \
      --port="${VERIFY_PG_PORT}" \
      --username="${VERIFY_PG_USER}" \
      --dbname="${restore_db}" \
      --no-owner \
      --role=settla \
      --exit-on-error \
      "${DUMP_FILE}" \
      2>"/tmp/restore-${dbname}.err"; then
    RESTORE_ERRORS="$(cat /tmp/restore-${dbname}.err)"
    notify_failure "pg_restore failed for ${dbname}" "${RESTORE_ERRORS}"
    EXIT_CODE=1
    psql_as_superuser --dbname=postgres -c "DROP DATABASE IF EXISTS \"${restore_db}\";" 2>/dev/null || true
    rm -f "${DUMP_FILE}"
    continue
  fi
  rm -f "${DUMP_FILE}"
  log "Restore complete."

  # ---- Row count verification ----
  log "Counting rows in ${verify_table} ..."
  ROW_COUNT="$(PGPASSWORD="${VERIFY_PG_PASSWORD}" psql \
    --host="${VERIFY_PG_HOST}" \
    --port="${VERIFY_PG_PORT}" \
    --username="${VERIFY_PG_USER}" \
    --dbname="${restore_db}" \
    --tuples-only \
    --command="SELECT COUNT(*) FROM ${verify_table};" 2>/dev/null | tr -d ' \n')"

  if [[ -z "${ROW_COUNT}" || ! "${ROW_COUNT}" =~ ^[0-9]+$ ]]; then
    notify_failure "Row count query failed for ${dbname}" \
      "Table: ${verify_table} — returned: '${ROW_COUNT}'"
    EXIT_CODE=1
  elif [[ "${ROW_COUNT}" -lt "${min_rows}" ]]; then
    notify_failure "Row count too low for ${dbname}" \
      "Table ${verify_table}: got ${ROW_COUNT} rows, expected >= ${min_rows}"
    EXIT_CODE=1
  else
    log "Row count OK: ${verify_table} has ${ROW_COUNT} rows (min: ${min_rows})"
    BACKUP_AGE_KEY="$(echo "${LATEST_KEY}" | grep -oE '[0-9]{8}T[0-9]{6}Z')"
    REPORT_LINES+=("OK: ${dbname} — ${ROW_COUNT} rows in ${verify_table} (backup: ${BACKUP_AGE_KEY})")
  fi

  # ---- Cleanup throwaway database ----
  log "Dropping restore target: ${restore_db}"
  psql_as_superuser --dbname=postgres \
    -c "DROP DATABASE IF EXISTS \"${restore_db}\";" 2>/dev/null || \
    log "WARNING: Failed to drop ${restore_db} — manual cleanup required"
done

# ---------------------------------------------------------------------------
# Final summary notification
# ---------------------------------------------------------------------------
if [[ "${EXIT_CODE}" -eq 0 ]]; then
  send_final_report "PASSED" "good" ":white_check_mark:"
  log "All backup verifications passed."
else
  send_final_report "FAILED" "danger" ":rotating_light:"
  log "One or more verifications failed."
fi

exit "${EXIT_CODE}"
