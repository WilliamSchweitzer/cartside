#!/bin/sh
# Preserve the shared idempotent provisioner; add deployment-only isolation.
set -eu
/bin/sh /provision-keycloak-db.sh
psql -X -q -v ON_ERROR_STOP=1 <<'SQL'
REVOKE ALL ON DATABASE keycloak FROM PUBLIC;
SQL
