#!/usr/bin/env sh
# ==============================================================================
# MyCaffe POS Platform - PostgreSQL 18 Triple-Role Initialization Script
# Mounted at: /docker-entrypoint-initdb.d/00-init-roles.sh
#
# Creates two non-superuser operational roles:
# 1. mycaffe_migrator : Schema owner & migration role (NOSUPERUSER, NOBYPASSRLS)
# 2. mycaffe_app      : Runtime API role (NOSUPERUSER, NOBYPASSRLS, no DELETE on ledgers)
# ==============================================================================
set -eu

MIGRATOR_USER="${DB_MIGRATOR_USER:-mycaffe_migrator}"
MIGRATOR_PASS="${DB_MIGRATOR_PASSWORD:-}"
APP_USER="${DB_APP_USER:-mycaffe_app}"
APP_PASS="${DB_APP_PASSWORD:-}"
TARGET_DB="${POSTGRES_DB:-mycaffe_pos}"

if [ -z "$MIGRATOR_PASS" ] || [ -z "$APP_PASS" ]; then
  echo "[POSTGRES INIT ERROR] DB_MIGRATOR_PASSWORD and DB_APP_PASSWORD must be set." >&2
  exit 1
fi

psql -v ON_ERROR_STOP=1 \
  --username "$POSTGRES_USER" \
  --dbname "$TARGET_DB" \
  --set=migrator_user="$MIGRATOR_USER" \
  --set=migrator_pass="$MIGRATOR_PASS" \
  --set=app_user="$APP_USER" \
  --set=app_pass="$APP_PASS" \
  --set=target_db="$TARGET_DB" <<'EOSQL'
SELECT
    set_config('app.init_migrator_user', :'migrator_user', false),
    set_config('app.init_migrator_pass', :'migrator_pass', false),
    set_config('app.init_app_user', :'app_user', false),
    set_config('app.init_app_pass', :'app_pass', false),
    set_config('app.init_target_db', :'target_db', false);

DO $$
DECLARE
    m_user TEXT := current_setting('app.init_migrator_user', true);
    m_pass TEXT := current_setting('app.init_migrator_pass', true);
    a_user TEXT := current_setting('app.init_app_user', true);
    a_pass TEXT := current_setting('app.init_app_pass', true);
    t_db   TEXT := current_setting('app.init_target_db', true);
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = m_user) THEN
        EXECUTE format('CREATE ROLE %I WITH LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS', m_user, m_pass);
    ELSE
        EXECUTE format('ALTER ROLE %I WITH LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS', m_user, m_pass);
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = a_user) THEN
        EXECUTE format('CREATE ROLE %I WITH LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS', a_user, a_pass);
    ELSE
        EXECUTE format('ALTER ROLE %I WITH LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS', a_user, a_pass);
    END IF;

    EXECUTE format('GRANT ALL PRIVILEGES ON DATABASE %I TO %I', t_db, m_user);
    EXECUTE format('ALTER SCHEMA public OWNER TO %I', m_user);
    EXECUTE format('GRANT ALL ON SCHEMA public TO %I', m_user);

    EXECUTE format('GRANT CONNECT ON DATABASE %I TO %I', t_db, a_user);
    EXECUTE format('GRANT USAGE ON SCHEMA public TO %I', a_user);
    EXECUTE format('GRANT %I TO %I WITH INHERIT FALSE, SET TRUE', a_user, m_user);
END $$;

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
EOSQL

echo "[POSTGRES INIT] Non-superuser roles '${MIGRATOR_USER}' and '${APP_USER}' initialized on database '${TARGET_DB}'."
