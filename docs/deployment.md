# Single ARM64 VM demo package

This milestone packages Cartside; it creates no Oracle resources and publishes
nothing. The host-based workflow in `compose.yaml` is unchanged. Use
`compose.deploy.yaml` **alone**, with its own project name, environment file and
fresh volumes. Never combine the two Compose files or reuse development volumes,
database dumps, users, credentials or `.env` values.

## Architecture and hostnames

Plan `https://cartside.wschweitzer.com` for the application and
`https://auth.cartside.wschweitzer.com` for identity. Both will need DNS A records
pointing at the VM; publish AAAA only after IPv6 routing/firewalls are verified.
DNS changes and certificate issuance are future work, outside this milestone.

A non-root nginx edge terminates TLS on container port 8443, published as host
443. Supply a certificate covering **both** names and its key in
`.deploy/certs/fullchain.pem` and `.deploy/certs/privkey.pem`. No ACME client runs
and no certificates are requested by these files. Only this HTTPS port is
published, bound to loopback by default. There is no port 80 redirect/listener.
A future certificate renewal mechanism must be selected, tested and monitored;
nginx must reload after renewal. Mount files read-only, restrict directory access,
and grant container UID 101 read access to the key without making it world-readable.

Edge routes the application to APISIX, which routes to API/static frontend.
Only `/realms/cartside/` and `/resources/` reach Keycloak on the identity host.
`/admin`, `/realms/master`, management health/metrics and other identity routes
are denied. The API, PostgreSQL, mock provider and monitoring have no host ports.
The edge also blocks application `/internal`, `/metrics`, `/apisix` and `/admin`.
Application APIs intentionally remain available **through authenticated APISIX**.
APISIX administration is disabled; its control listener is container loopback.
Only edge joins the external bridge; all service traffic uses the internal
Compose network. Docker host/root and members of that network are trusted.

nginx overwrites Host and X-Forwarded-{Host,Proto,Port,For}, removes Forwarded and
X-Forwarded-Prefix, and does not manufacture Origin/CSRF headers. Only edge is
reachable from outside; Keycloak uses `KC_PROXY_HEADERS=xforwarded` with a fixed
HTTPS hostname and strict hostname checks. APISIX receives the same fixed HTTPS
origin, has explicit callback/logout URLs and Secure/HttpOnly/SameSite=Lax session
cookies. Do not add another proxy without revisiting this trust boundary.
See [Keycloak proxy guidance](https://www.keycloak.org/server/reverseproxy) and
[hostname/backchannel guidance](https://www.keycloak.org/server/hostname).

Keycloak's canonical issuer remains
`https://auth.cartside.wschweitzer.com/realms/cartside`. Dynamic backchannel
resolution lets APISIX use internal HTTP discovery/token endpoints while browser
sign-in retains the public HTTPS hostname. Go uses the private JWKS endpoint but
verifies the canonical issuer and API audience independently. The realm retains
`sslRequired=external`: private backchannel HTTP is intentional; public access is
HTTPS only. TLS verification, PKCE, nonce, redirect restrictions, issuer checking
and Go's origin/header CSRF rules remain enabled. This internal/public discovery
behavior must pass the HTTPS runner before launch; never work around failures by
disabling validation. See [APISIX OIDC settings](https://apisix.apache.org/docs/apisix/plugins/openid-connect/).

## Prerequisites and configuration

Use a native linux/arm64 Docker engine with Compose v2/buildx, sufficient free disk,
Python 3 with `scripts/requirements-ci.txt`, and openssl. Browser verification also
needs Node 24, `npm ci` in `frontend`, and Playwright Chromium
(`npx playwright install chromium`). Docker builds use pinned Go 1.27.3 and Node
24.15.0; the mock builder now matches the API builder, preserving its
scratch/non-root runtime. Go 1.27.3 includes the latest reviewed crypto/x509
security patch ([release history](https://go.dev/doc/devel/release#go1.27.3)). The application module
still declares Go 1.24 compatibility.

From the repository root:

```sh
mkdir -p .deploy/certs
cp infra/deploy/.env.example .deploy/demo.env
chmod 700 .deploy
chmod 600 .deploy/demo.env
```

Replace every `replace_with_...` value with a **different** cryptographically random
64-character hex secret (for example, write `secrets.token_hex(32)` from Python
directly to the private file). Use new deployment-specific credentials; hex values
also avoid PostgreSQL URL escaping and Compose interpolation surprises. Keep
`.deploy/` untracked. Never put secrets in frontend variables or Docker build args.
The template is documentation, not runnable credentials. Compose environment
values are visible to Docker administrators; choose a stronger secret delivery
mechanism if the trusted-host assumption changes. Do not print rendered Compose
configuration or enable HTTP/browser debug logging containing sessions.

Use a unique immutable `IMAGE_TAG` for each release. All commands below use:

```sh
dc() { docker compose --env-file .deploy/demo.env -p cartside-demo -f compose.deploy.yaml "$@"; }
dc config --quiet
python3 scripts/check-deployment.py --manifests
dc build
```

The image manifest check includes every required base/runtime image and optional
monitoring images. `platform: linux/arm64` is explicit; do not enable emulation to
work around missing architecture support. For reproducible releases record image
IDs/digests, source revision, configuration revision and database migration version;
archive/export or privately store all three application images. Version tags alone
are mutable, so never overwrite a release tag. Debian's runtime manifest is pinned
by digest; other images use exact version tags and need digest locking in the
release record before launch. Refresh supported patch releases deliberately.

## Fresh database and identity provisioning

Do not start the entire stack before these explicit initialization steps:

```sh
dc up -d --wait postgres
dc run --rm --no-deps app-db
dc run --rm --no-deps keycloak-db
dc up -d --wait keycloak mock-ai
dc run --rm --no-deps migrate
dc run --rm --no-deps --entrypoint seed migrate
# Apply explicit runtime grants after creating/updating the schema:
dc run --rm --no-deps app-db
dc up -d --wait api frontend apisix
# Only after certificates have been supplied out of band:
dc up -d --wait edge
```

`keycloak-db` creates/updates the separate Keycloak role and database. Keycloak
runs production `start` with PostgreSQL and imports the deployment realm **only
when it does not exist**. No users are imported. The confidential OIDC client secret
is resolved from the Keycloak environment. The realm enables self-registration and
brute-force protection. The sample catalog seed inserts deterministic demo products;
it does not copy development data. Migrations are explicit, not API startup work.

`POSTGRES_USER` is bootstrap administration only. `app-db` reproducibly creates
`cartside_migrate` and `cartside_runtime` with independent deployment passwords.
The `migrate` tools-profile service receives only the migration database URL;
the API receives only the runtime URL. Neither identity is superuser, creates
roles, replicates, bypasses RLS or inherits another role. Keycloak remains a
separate role/database; deployment bootstrap revokes PUBLIC access to its database. No runtime schema creation, temporary tables, migration
metadata access, catalog price/content writes or order history mutation is granted.
Runtime can read products, create/version carts, mutate cart items, and insert/read
orders/snapshots. PostgreSQL requires an UPDATE privilege for `SELECT FOR SHARE`;
`UPDATE(id)` on products supplies the narrow column privilege needed for checkout
locking. It does permit direct ID updates by a compromised runtime identity; no
catalog write endpoint exists, and catalog content/price columns stay protected.

For an existing deployment database, stop API mutations and take a verified backup,
then run the same app-db → migrate → app-db sequence without dropping databases or
volumes. Bootstrap transfers only the named application tables and immutable-order
trigger function to the migration role; unrelated objects are not reassigned.
It revokes runtime table/sequence privileges before applying explicit current grants.
The transaction preserves rows and supports repeated execution. Migration/bootstrap
credentials belong only in operator tooling, never API environment or browser bundles.
Run app-db after every migration; new tables get no automatic runtime permissions.

Realm import does **not** update existing realms. For later client secret/redirect,
registration or policy changes, use authenticated private administration and verify
those changes explicitly. Bootstrap administrator values only initialize an absent
admin; changing the environment is not password rotation. Remove bootstrap credentials
from routine operation after creating and testing a private administrator/recovery
procedure. Do not delete identity volumes to apply a configuration change.

No browser administrative console is exposed. Use the bundled `kcadm.sh` through
`dc exec keycloak bash`, targeting `http://127.0.0.1:8080`, realm `master` and a
private administrator; let the CLI prompt for its password instead of placing it
in shell history. Clear its saved session afterward. VM access will require private
SSH access. A browser console, if later needed, requires a separately designed
SSH-tunnel/private HTTPS route; never expose `/admin` or the master realm here.

## Verification before provisioning

```sh
# Use a Python environment containing the pinned CI PyYAML dependency.
python3 scripts/check-deployment.py --manifests
python3 scripts/validate-config.py
# Build IMAGE_TAG=demo-001 for this disposable runner; do not reuse real credentials.
# It generates its own random Compose project, secrets, users and local TLS cert.
python3 scripts/verify-deployment.py
```

The runner defaults to locally built `cartside-{api,frontend,mock}:demo-001` images.
Set `CARTSIDE_DEPLOY_IMAGE_TAG` to verify a different immutable local build tag.
It uses loopback port 18443 and `cartside.localhost` / `auth.cartside.localhost`
(Chromium resolves localhost subdomains to loopback). The runner also supplies a
process-local Node DNS hook for exactly those two disposable names, covering both
callback and promise lookups used by Playwright. It does not modify system DNS,
`/etc/hosts`, public names or production issuer/certificate checks. A port collision must fail;
never stop an existing service to make room. It removes only its random project's
containers/volumes and temporary private files. It never sources `.env` or
`.deploy/demo.env`. Test certificate verification is bypassed **only in this
explicit browser runner**; production OIDC TLS verification stays enabled.

The existing five browser scenarios cover login/logout, catalog/static assets,
cart isolation, mock suggestions and explicit acceptance, checkout atomicity/retry
and conflicts. Deployment mode replaces Vite HMR expectations with secure cookie,
CSRF rejection and edge route denial assertions. The deployment browser scenario verifies identity-host
`/admin/`, `/realms/master/`, `/health/ready` and `/metrics` return 404, discovery
returns the public issuer, public redirects never contain internal service names,
and signed-out APIs return 401. The runner inspects health and published ports,
verifies migration ownership/runtime denial and an upgrade from bootstrap-owned
tables, repeats browser flows after restart, starts private monitoring and checks
both Prometheus targets, then checks clean shutdown exits. It samples container
resources by phase and records disk use. Sampling can miss transient peaks. Keep host/service logs and browser
artifacts private; tests suppress sensitive error details.

The earlier disk-exhaustion/build-cache failure was resolved without removing
developer volumes. Native builds and the disposable HTTPS stack have since run;
see [the dated verification record](deployment-verification.md) for executed
checks, exact image digests, sampled resources and remaining limits. The record
supersedes the earlier incomplete-build evidence; it is not a public-launch approval.

## Operations and launch limits

All long-running services restart unless stopped. Health checks distinguish process,
PostgreSQL readiness and HTTP readiness; they do not automatically repair unhealthy
services. Graceful stop allows 30 seconds (Go allows ten seconds for HTTP draining).
Container JSON logs rotate at 10 MB × 3 files each; application/gateway logs exclude
credentials and prompt bodies. Edge access logs are disabled to avoid OIDC query
logging. Review startup/error logs privately too.

Estimate 2–4 GiB steady-state RAM without monitoring, 1–2 additional GiB for
Prometheus/Grafana, and significantly more transient space/RAM for builds. These
are planning estimates, not measurements or Oracle capacity guarantees. Measure
`docker stats --no-stream`, `docker system df`, `df -h`, VM memory and swap during
startup and browser load; record peak RAM/CPU, disk growth and response latency.
Keycloak heap is bounded at 768 MiB, but total process memory exceeds heap.
No container-wide memory/CPU limits have been tuned. Prefer building release images
before moving them to a small VM. Do not prune shared Docker volumes to free space.

Monitoring is off by default. To enable, create a fresh private
`.deploy/grafana_admin_password` readable by Grafana UID 472, then:

```sh
dc --profile observability up -d prometheus grafana
```

Neither service publishes ports. Access requires a separately controlled private
path; do not add them to edge. API metrics and APISIX exporter are private even
when monitoring is disabled. Prometheus retains up to 7 days/1 GB. No Grafana
password file is needed for the normal stack.

Self-registration consumes public resources: accounts, password hashing, database
storage, sessions, carts/orders and requests. Current protections include verified
identity/ownership, CSRF, Keycloak brute-force protection, request/body limits and
Go's per-user assistant quota (10/minute, one in flight, 1000 user entries). Those
quotas reset on restart and the deterministic mock incurs no live-model charges.
There is no CAPTCHA, verified-email requirement, global registration quota,
account/storage retention policy or comprehensive edge abuse/DoS control. Decide
and implement appropriate registration/traffic limits before public launch, or
disable self-registration and use explicitly provisioned demo accounts. The API uses the restricted runtime role described above; migrations and bootstrap
remain explicit operator privileges.

For rollback, stop mutations, preserve the current release and take a verified
backup before migration. Confirm old binaries/config remain compatible with the
new schema. Revert immutable image/config versions only when compatible; an image
rollback **does not reverse migrations**. Otherwise restore a tested database
backup with the corresponding images/config/identity state during a maintenance
window. Never run `migrate down` blindly: down migrations can destroy data.

**Backups do not exist as part of this milestone.** Before launch, implement and
test consistent PostgreSQL backup/restore for both application and Keycloak data,
secure off-VM retention/encryption, credentials/configuration recovery, volume
failure recovery, and a full restore rehearsal with recorded RPO/RTO. Merely
having a named volume or copying a live data directory is not a backup strategy.

Remaining public-launch gates (packaging verification does not authorize launch):

1. Implement and rehearse application/Keycloak backup and restore, off-VM retention,
   configuration recovery, and release/migration rollback with recorded RPO/RTO.
2. Decide public registration protections or disable registration; define traffic,
   account/storage retention and resource-abuse controls.
3. Review secret delivery/rotation, private administrator recovery and removal of
   bootstrap credentials; lock release dependencies and review patches again at launch.
4. Select VM sizing and verify cloud availability/limits using target-host testing;
   local resource samples do not establish production capacity.
5. Review DNS, firewall (443 and restricted SSH), certificate delivery/renewal and
   private administration. Obtain separate authorization for Oracle provisioning,
   DNS changes, public certificate issuance and public deployment.

This package performs no Oracle provisioning, DNS changes, public certificate
requests or public deployment. Those actions require separate authorization.
