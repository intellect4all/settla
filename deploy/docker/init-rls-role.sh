#!/bin/bash
# Docker entrypoint init script: creates the settla_app role used by RLS.
# Mounted as /docker-entrypoint-initdb.d/init-rls-role.sh on all 3 Postgres containers.
# Only runs on first container initialization (empty data dir).
#
# The password is read from the SETTLA_APP_DB_PASSWORD environment variable
# (default: settla_app). CHANGE IN PRODUCTION.

set -e

APP_PASSWORD="${SETTLA_APP_DB_PASSWORD:-settla_app}"

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
  DO \$\$ BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'settla_app') THEN
      CREATE ROLE settla_app LOGIN PASSWORD '${APP_PASSWORD}';
    END IF;
  END \$\$;
EOSQL
