#!/bin/sh
# Runs against fresh AND initialized PostgreSQL volumes. Never trace secrets.
set -eu
if [ "$PGUSER" = keycloak ] || [ "$PGDATABASE" = keycloak ]; then
  echo 'Keycloak requires a separate role and database from the application.' >&2
  exit 1
fi
psql -X -q -v ON_ERROR_STOP=1 <<'SQL'
\getenv kc_password KEYCLOAK_DB_PASSWORD
-- Keep password-bearing statements out of server statement/error logs.
SET log_statement = 'none';
SET log_min_error_statement = 'panic';
SELECT 'CREATE ROLE keycloak LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE'
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'keycloak') \gexec
SELECT format('ALTER ROLE keycloak PASSWORD %L', :'kc_password') \gexec
SELECT 'CREATE DATABASE keycloak OWNER keycloak'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'keycloak') \gexec
SQL
