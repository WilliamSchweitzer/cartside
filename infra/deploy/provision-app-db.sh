#!/bin/sh
# Explicit deployment bootstrap, safe on existing volumes; never trace credentials.
set -eu
[ "$PGUSER" != cartside_runtime ] && [ "$PGUSER" != cartside_migrate ] && [ "$PGDATABASE" != keycloak ]
psql -X -q -v ON_ERROR_STOP=1 <<'SQL'
\getenv runtime_password APP_RUNTIME_PASSWORD
\getenv migration_password APP_MIGRATION_PASSWORD
SET log_statement = 'none';
SET log_min_error_statement = 'panic';
BEGIN;
SELECT 'CREATE ROLE cartside_migrate LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE'
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname='cartside_migrate') \gexec
SELECT 'CREATE ROLE cartside_runtime LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE'
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname='cartside_runtime') \gexec
ALTER ROLE cartside_migrate NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
ALTER ROLE cartside_runtime NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
SELECT format('ALTER ROLE cartside_migrate PASSWORD %L', :'migration_password') \gexec
SELECT format('ALTER ROLE cartside_runtime PASSWORD %L', :'runtime_password') \gexec
-- No role memberships are required or permitted for these deployment identities.
SELECT format('REVOKE %I FROM %I', parent.rolname, child.rolname)
FROM pg_auth_members m JOIN pg_roles parent ON parent.oid=m.roleid
JOIN pg_roles child ON child.oid=m.member
WHERE child.rolname IN ('cartside_migrate','cartside_runtime') \gexec
SELECT format('REVOKE ALL ON DATABASE %I FROM PUBLIC, cartside_runtime', current_database()) \gexec
SELECT format('GRANT CONNECT ON DATABASE %I TO cartside_runtime', current_database()) \gexec
SELECT format('GRANT CONNECT, CREATE ON DATABASE %I TO cartside_migrate', current_database()) \gexec
REVOKE ALL ON SCHEMA public FROM PUBLIC, cartside_runtime;
ALTER SCHEMA public OWNER TO cartside_migrate;
GRANT USAGE ON SCHEMA public TO cartside_runtime;
-- Upgrade only known application objects; do not reassign unrelated/bootstrap data.
SELECT format('ALTER TABLE public.%I OWNER TO cartside_migrate', tablename)
FROM pg_tables WHERE schemaname='public' AND tablename IN
('products','carts','cart_items','orders','order_items','goose_db_version') \gexec
SELECT 'ALTER FUNCTION public.reject_order_change() OWNER TO cartside_migrate'
WHERE to_regprocedure('public.reject_order_change()') IS NOT NULL \gexec
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM cartside_runtime;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM cartside_runtime;
-- Explicit grants after migration; future tables receive no automatic runtime rights.
SELECT 'GRANT SELECT, UPDATE(id) ON public.products TO cartside_runtime'
WHERE to_regclass('public.products') IS NOT NULL \gexec
SELECT 'GRANT SELECT, INSERT, UPDATE(version) ON public.carts TO cartside_runtime'
WHERE to_regclass('public.carts') IS NOT NULL \gexec
SELECT 'GRANT SELECT, INSERT, UPDATE(quantity), DELETE ON public.cart_items TO cartside_runtime'
WHERE to_regclass('public.cart_items') IS NOT NULL \gexec
SELECT format('GRANT SELECT, INSERT ON public.%I TO cartside_runtime', tablename)
FROM pg_tables WHERE schemaname='public' AND tablename IN ('orders','order_items') \gexec
COMMIT;
SQL
