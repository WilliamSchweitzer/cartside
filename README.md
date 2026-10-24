# Cartside

A conversational grocery storefront built with Go, React, PostgreSQL, and Apache APISIX.

Cartside lets customers browse groceries, search for products, and build a cart manually or with help from an AI shopping assistant. The assistant suggests products from the catalog, and customers accept or decline each suggestion before anything is added to their cart.

This is a personal learning project focused on Go application design, API gateway policies, OIDC integration, and AI provider integration.

## Project status

Implemented: the Go/Chi server, public liveness and PostgreSQL readiness, shared pgx v5 pool, graceful shutdown, local PostgreSQL/APISIX/Keycloak infrastructure, and APISIX-managed OIDC registration/sign-in and logout. React provides a searchable grocery storefront with cart additions, quantity updates, removal, secondary backend diagnostics, and logout; it stores no tokens. APISIX protects the frontend and non-health APIs with a browser session and provides a gateway-only session check. Go verifies access tokens and exposes the current subject/issuer through `/api/me`; React displays that identity. CI checks Go, frontend, and configuration independently. Phase 2 milestones 1–3 are implemented: catalog schema migrations, 100 reproducible development products, an authenticated read-only catalog API with search and pagination, and a responsive catalog UI. User-owned cart persistence and Go operations are also implemented. Authenticated cart HTTP endpoints with CSRF protection are implemented. The cart UI is implemented. Simulated order persistence and transactional checkout Go operations are implemented. The simulated-checkout HTTP endpoint and review/confirmation UI with session-scoped same-key recovery are implemented. Order browsing and other business modules remain planned. The AI milestones provide an internal service-authenticated APISIX → deterministic mock provider path and a catalog-grounded Go shopping assistant. The assistant supports three explicit demo scenarios, validates structured suggestions, and refreshes authoritative catalog data. The authenticated, CSRF-protected assistant HTTP endpoint is implemented with bounded per-user limits and is disabled by default. The embedded demo assistant and individual suggestion acceptance are implemented; see [the AI guide](docs/ai-mock.md). Request correlation and safe JSON request/assistant logs are implemented across APISIX, Go, and the mock provider; see [the observability guide](docs/observability.md). Optional local Prometheus metrics, five tested local alert rules and a provisioned Grafana overview are implemented; distributed tracing remains planned. Unless stated otherwise, the architecture below describes the intended design.

## Mock-assisted MVP verification

The original local mock-assisted MVP exit criteria passed on **2026-10-23**:
all five isolated Chromium tests, six PostgreSQL integration suites with race
detection, Go formatting/vet/unit/race/build, frontend lint/type-check/86 tests/build,
and isolated gateway/configuration checks. The browser flow verifies suggestions
alone leave data unchanged, exact-quantity acceptance without double-addition,
Decline, mixed manual shopping, explicit checkout, order snapshots, cart clearing,
and logout. Focused tests cover assistant failures and checkout coordination.
No reproducible application defect remains in the exercised flow.

This completes the deterministic three-scenario local demo. Broader language
understanding requires a real model and separate verification; no live-provider
compatibility or production readiness is claimed. See [exact evidence and
limitations](docs/e2e.md#complete-mock-assisted-mvp-verification-2026-10-23).
The records below describe earlier milestones.

## Non-AI stabilization gate

The non-AI shopping foundation passed the stabilization gate on **2026-10-18**.
The explicit [end-to-end guide](docs/e2e.md) describes the disposable environment,
coverage, limitations, and recovery contract. The full-stack suite remains outside
ordinary CI. The first deterministic mock AI protocol milestone is now implemented; see [local AI verification](docs/ai-mock.md).

This review fixed indefinite locking after a marked same-key rejection, prevented
unmarked gateway 403 responses from discarding recovery, rejected ambiguous stored
request bodies, and scoped shopping state to the verified identity with guards
against late checkout responses.

Verification: Go 1.24.0 formatting, vet, unit tests, race tests, and build passed.
Node 24.15.0 frontend lint, application/E2E type-check, 46 unit tests, and production
build passed. All five existing integration packages (catalogseed, catalog, cart,
orders, checkout HTTP) passed with the race detector against separate fresh
databases. All five Chromium shopping tests passed against a new Compose project
running PostgreSQL 17.10, APISIX 3.14.1, and Keycloak 26.8.1 with disposable users.
Committed-response loss/replay, reload/session renewal, user isolation, failed cart
refresh after success, and cart/price/availability conflicts were exercised live.
JSON/YAML parsing, Compose startup validation, and diff whitespace checks passed.
The runner removed its containers, volumes, and private configuration afterward;
developer configuration and data were preserved. Earlier milestone notes below
remain historical evidence.

Remaining verification limitations: passive refresh-token expiry, screen readers,
and browsers/devices beyond Chromium's desktop and 375px viewport checks were not
exercised. Product limitations remain tab-scoped recovery, no confirmation history
after a resolved reload, and no UI coordination across tabs/devices. These are
separate from defects in the verified non-AI flow.

## MVP

- Open registration and sign-in, required to access the storefront and chat.
- A fixed sample grocery catalog with product images, prices, and search.
- A separate cart for each customer, with manual quantity changes and item removal.
- Immediate cart updates within the current browser.
- An embedded AI chat that recommends catalog products based on customer requests.
- Individual acceptance or rejection of each suggested item.
- Simulated checkout that saves an order, returns a confirmation number, and clears the cart.

Real payments, delivery integrations, live inventory, and synchronization across tabs or devices are outside the initial scope.

## Architecture

```mermaid
flowchart TD
    B["Browser"] -->|"One application origin"| A["Apache APISIX"]
    A -->|"/"| R["React frontend"]
    A -->|"/api"| G["Go backend"]
    B <-->|"Sign-in and registration redirects"| K["Keycloak"]
    A <-->|"OIDC discovery and token exchange"| K
    G --> P["Application PostgreSQL database"]
    G -->|"Internal AI route"| A
    A -->|"Provider request"| M["AI model provider"]
```

The incoming application routes and internal AI route have separate policies, initially within one APISIX instance. Keycloak uses its own database or isolated database configuration; application modules do not access its credential storage.

### Responsibilities

| Component | Responsibility |
| --- | --- |
| React | Storefront, search, cart, chat, suggestion acceptance, and checkout UI |
| APISIX | Application routing, OIDC login flow and session enforcement, AI routing, and request limits |
| Keycloak | Registration, credentials, and user authentication |
| Go | Application authorization, business rules, catalog access, carts, orders, and AI conversation coordination |
| PostgreSQL | Persistent application data |
| AI provider | Generate shopping suggestions using catalog context supplied by Go |

### Authentication and access

APISIX manages the OIDC flow with Keycloak and maintains the browser session. React does not manage OIDC tokens. Go independently verifies the access token forwarded by APISIX and serves the current identity. Cart operations scope ownership to the verified issuer and subject; cart HTTP endpoints enforce this identity; checkout scopes orders to that same verified identity; order browsing and its HTTP authorization remain planned.

Go rejects forged identity headers and verifies signatures and claims independently. A valid access token can authenticate directly to Go; network isolation remains planned. Session configuration and state-changing requests must account for cookie security and CSRF protection. Registration, authentication callbacks, and operational health routes require deliberate routing exceptions.

The AI provider key belongs to APISIX. The internal AI route must require service authentication or internal access restrictions; a storefront session alone does not grant access to it. Per-user AI limits must use a verified identity propagated through the backend.

## Go backend

The backend will be a modular monolith: one Go application with five internal business modules. Currently, `internal/auth` verifies access tokens and provides a minimal principal, `cmd/api` manages process startup and shutdown, `internal/database` validates database configuration and creates the pool, and `internal/httpapi` serves public health, protected current-user, and catalog routes. `internal/catalog` owns the runtime product model and PostgreSQL read repository, independently of the development seed package. `internal/cart` owns persistent user carts, quantity rules, versioning, and current-price totals; its HTTP adapter lives in `internal/httpapi`. Pool creation does not wait for PostgreSQL. The process closes the shared pool after HTTP shutdown.

| Module | Responsibility |
| --- | --- |
| Accounts | Application user records and association with the identity provider |
| Catalog | Product data, search, and authoritative product prices |
| Cart | Cart ownership, items, quantities, and cart versions |
| Orders | Checkout coordination, order records, and confirmations |
| Shopping assistant | Conversation coordination, catalog context, and product suggestions |

Modules interact through defined operations rather than directly modifying another module's tables. Manual additions and accepted AI suggestions use the same cart operations. The model does not control authoritative product IDs, prices, or ownership decisions.

### Checkout consistency

The implemented orders module coordinates simulated checkout with cart and catalog operations.

- Save the order and clear the cart in one database transaction.
- Preserve the cart if the transaction fails.
- Associate each checkout attempt with an idempotency key scoped to the user.
- Return the existing confirmation when a completed attempt is retried.
- Associate checkout with the cart version the customer reviewed; reject stale attempts.
- Serialize checkout and cart mutations through a consistent cart-locking strategy.

Retrying a completed checkout uses the same key and original input, even if the cart has since changed. A newly reviewed cart requires a new checkout attempt. Database constraints and transaction boundaries enforce these rules alongside application validation.

## Repository organization

One repository contains the frontend, backend, infrastructure configuration, and documentation. The current layout is:

| Path | Contents |
| --- | --- |
| `frontend/` | React and TypeScript application |
| `backend/` | Go application and internal modules |
| `infra/` | APISIX and Keycloak configuration and supporting infrastructure |
| `.github/workflows/` | CI checks |
| `scripts/` | CI configuration parser and its pinned dependency |
| `compose.yaml` | Local infrastructure services |
| `.env.example` | Required local configuration with safe placeholders |

## Local development

React and Go are intended to run directly on the developer's machine. Docker Compose runs PostgreSQL, APISIX, Keycloak, and a deterministic local mock AI provider. React runs on the host with Vite.

Open **http://localhost:9080**. APISIX routes `/api/*` to Go with `/api` removed and storefront paths to the host-run Vite server. `/internal/*` is reserved; only the service-authenticated AI route is enabled there. The frontend calls relative `/api/products`, `/api/cart`, `/api/assistant/messages`, `/api/me`, and `/api/health/ready` URLs; no separate browser API origin or CORS configuration is needed. The main screen browses and searches groceries; readiness and its Retry action are under Service diagnostics. Application access requires Keycloak sign-in. The cart section supports manual shopping; simulated checkout supports explicit review, confirmation, and recovery of uncertain attempts. See the gateway authentication section below.

### Configuration

- Keep local configuration in an untracked `.env` file.
- Commit `.env.example` with required names, descriptions, and placeholders.
- Validate required configuration at service startup.
- Keep credentials, API keys, and session secrets out of Git and browser bundles.
- Choose deployment secret storage when the hosting environment is selected.

### Setup instructions

Use **Node.js 24 LTS** (24.15 or later within 24.x, including npm) for the frontend and Go 1.24.0 (matching `backend/go.mod`) for the backend; Docker Compose runs PostgreSQL 17.10. From the repository root:

```sh
cp .env.example .env
# Generate local secrets (Python 3); nothing secret is printed.
python3 - <<'PYCODE'
from pathlib import Path
import secrets
p = Path('.env')
lines = p.read_text().splitlines()
for key in ('POSTGRES_PASSWORD', 'KEYCLOAK_DB_PASSWORD', 'KEYCLOAK_ADMIN_PASSWORD', 'CARTSIDE_OIDC_CLIENT_SECRET', 'CARTSIDE_SESSION_SECRET', 'CARTSIDE_AI_SERVICE_KEY'):
    matches = [i for i, line in enumerate(lines) if line.startswith(key + '=')]
    if not matches:
        lines.append(key + '=' + secrets.token_hex(32))
    elif lines[matches[0]].split('=', 1)[1].startswith('replace_with_'):
        lines[matches[0]] = key + '=' + secrets.token_hex(32)
if not any(line.startswith('KEYCLOAK_ADMIN_USERNAME=') for line in lines):
    lines.append('KEYCLOAK_ADMIN_USERNAME=local-admin')
# Synchronize only the example URL; preserve customized existing configuration.
values = dict(line.split('=', 1) for line in lines if '=' in line and not line.startswith('#'))
lines = [line.replace('replace_with_local_password', values['POSTGRES_PASSWORD'])
         if line.startswith('DATABASE_URL=') else line for line in lines]
p.write_text('\n'.join(lines) + '\n')
p.chmod(0o600)
PYCODE
docker compose config --quiet
docker compose up -d --wait
# Explicitly export the trusted local file into this shell before running Go:
set -a
. ./.env
set +a
cd backend
# Override older .env files that still use 127.0.0.1:8080.
HTTP_ADDR=0.0.0.0:8080 go run ./cmd/api
```

Compose automatically reads the root `.env` for interpolation of PostgreSQL settings; it does not export variables into your shell. Go reads only its process environment and does not load `.env`. The shell commands above export `DATABASE_URL`, the required `BROWSER_ORIGIN` and `OIDC_*` verification settings, and the optional `HTTP_ADDR`. Only source a trusted file, using shell-compatible assignments. Alternatively, supply `DATABASE_URL` directly in the Go process environment.

`DATABASE_URL` is required and must be a PostgreSQL URL with a host, user, and database name. Missing or malformed configuration fails startup with a generic error. A database connection failure does not prevent startup. `HTTP_ADDR` defaults to `:8080`; the example binds all IPv4 interfaces so APISIX can reach Go. Local `sslmode=disable` is for this host-only development setup.

In a second terminal, from the repository root (do not source the root `.env` for Vite):

```sh
cd frontend
npm ci
npm run dev
```

Vite binds `0.0.0.0:5173` so the APISIX container can reach it. `strictPort` makes startup fail if 5173 is occupied. Open http://localhost:9080, including for HMR; do not use 5173 as the browser origin. Vite explicitly allows `localhost` and `host.docker.internal` (its built-in localhost/IP allowances also apply). The development WebSocket connects to `ws://localhost:9080` and APISIX forwards upgrades to Vite. The frontend route has a one-hour read inactivity timeout for development WebSockets; the API retains its existing timeouts and rewriting. Keep this development server on a trusted machine with inbound firewall restrictions, like Go.

Frontend configuration is committed in `frontend/vite.config.ts`; it needs no environment variables. Compose consumes root `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB`, and the Keycloak/gateway variables documented below; host-run Go consumes `DATABASE_URL`, `HTTP_ADDR`, `BROWSER_ORIGIN`, `OIDC_ISSUER`, `OIDC_AUDIENCE`, and `OIDC_JWKS_URL`. Compose does not run or configure Vite, and bind-mounted APISIX YAML is not interpolated. Never copy database credentials or other secrets into frontend `.env` files or `VITE_*` variables: those values can enter browser bundles. Changing the Vite port also requires changing the APISIX frontend upstream; changing the gateway address requires updating the WebSocket client address.

For a production bundle:

```sh
cd frontend
npm ci
npm run lint
npm run typecheck
npm test
npm run build
```

The build writes static files to `frontend/dist/`. The separate [deployment package](docs/deployment.md) serves this bundle with nginx; the development gateway continues to use Vite.

In another terminal, from the repository root:

```sh
curl -i http://127.0.0.1:9080/api/health/live
curl -i 'http://127.0.0.1:9080/api/health/ready?probe=gateway'
docker compose ps
# This checks APISIX itself, without calling Go or PostgreSQL:
docker compose exec -T apisix resty -I /usr/local/apisix/deps/share/lua/5.1 /usr/local/apisix/healthcheck.lua
```

With PostgreSQL available, both health routes return HTTP 200 and `{"status":"ok"}`. APISIX strips `/api` from the path, preserves query strings, and passes upstream status codes and bodies through. The frontend catch-all requires sign-in. Bare `/api` and `/api/*` require a session and return 401 without one. After login, `/api/nonexistent` reaches Go and returns its 404 body.

### Local Keycloak identity provider

Keycloak is pinned to `quay.io/keycloak/keycloak:26.8.1`, the supported community release selected for this milestone. It runs **development mode only**, with HTTP and insecure development defaults. This is not a production configuration. APISIX now handles application authentication. No identity headers are trusted by Go, and React handles no tokens.

The setup block above generates independent application, Keycloak, and gateway secrets in the ignored `.env` and restricts it to mode 0600. It replaces missing values or example placeholders while preserving existing credentials. Python 3 is required. For an existing checkout, run only its Python block without copying `.env.example` over your configuration.

Keep the existing application credentials when upgrading an initialized checkout. `.env.example` is a template, not a source of working passwords. Compose passes Keycloak secrets as container environment variables; Keycloak itself resolves `${CARTSIDE_OIDC_CLIENT_SECRET}` in the mounted realm JSON using its supported import substitution. Compose does **not** interpolate mounted JSON or scripts. Do not run plain `docker compose config`, echo secrets, or publish container inspection output. Local Docker administrators can inspect container environments.

The one-shot `keycloak-db` job runs after PostgreSQL is healthy and before Keycloak starts. It creates the fixed `keycloak` login role (without superuser, database-creation, or role-creation privileges), creates the separate `keycloak` database owned by that role, and reconciles its password from `KEYCLOAK_DB_PASSWORD`. Neither application database nor application role may be named `keycloak`. The job can be rerun safely on an initialized `postgres_data` volume:

```sh
docker compose run --rm keycloak-db
```

Provisioning uses the official PostgreSQL image's local Unix-socket trust authentication and the existing `POSTGRES_USER` administrator. A separate `postgres_socket` volume shares that socket only with the provisioning job; Keycloak uses password authentication over TCP. The existing setup already uses `POSTGRES_USER` as the local application role and PostgreSQL bootstrap superuser. This milestone does not change that role or its password. Customized existing PostgreSQL installations that disable local trust or remove this administrator need equivalent administrator access for provisioning. Do not delete `postgres_data`; initialization scripts and `POSTGRES_*` changes do not update initialized databases. Adding the socket mount recreates the PostgreSQL container once, preserving its data volume.

`start-dev --import-realm` imports `infra/keycloak/cartside-realm.json` only if `cartside` does not exist. The realm allows registration. The confidential `cartside-apisix` client enables Authorization Code flow and client-secret authentication; implicit flow, direct password grants, and service accounts are disabled. Its only login redirect is **`http://localhost:9080/auth/callback`**, and its only post-logout redirect is **`http://localhost:9080/signed-out`**. There are no wildcard redirects or allowed browser web origins. APISIX handles both the callback and logout; neither reaches React or Go. Keycloak's built-in account-console client separately supports the account UI.

| Purpose | URL |
| --- | --- |
| Canonical issuer | `http://localhost:8180/realms/cartside` |
| Account console (registration and sign-in) | http://localhost:8180/realms/cartside/account/ |
| Local admin console | http://localhost:8180/admin/ |
| Browser discovery | http://localhost:8180/realms/cartside/.well-known/openid-configuration |
| APISIX discovery | `http://keycloak:8080/realms/cartside/.well-known/openid-configuration` |

Use `localhost` consistently in the browser, including for the application on port 9080. Keycloak publishes only `127.0.0.1:8180`; its management port 9000 is not published. `KC_HOSTNAME` fixes the public issuer and authorization URLs. `KC_HOSTNAME_BACKCHANNEL_DYNAMIC=true` lets discovery requested through Compose DNS advertise `http://keycloak:8080` token and JWKS endpoints while preserving the public issuer and browser authorization URL. APISIX requests discovery through `keycloak:8080`, then uses those internal token/key endpoints. Container `localhost` is the APISIX container itself, so it must not call `localhost:8180`. The internal discovery issuer remains the canonical public issuer; do not replace it with the internal address. The public issuer remains validated during Code flow; internal transport URLs do not change the issuer.

Health and metrics are enabled at startup (the stock image applies build options automatically). Metrics enable the database readiness check. The probe uses the supported `/health/ready` management endpoint on port 9000 and Bash TCP sockets because the selected image has Bash but no curl/wget. It checks HTTP 200, has a three-second Compose timeout, and was executed successfully in the pinned image. Readiness is informational; it does not automatically restart Keycloak.

### Keycloak changes, restarts, and verification

Realm and user data live in PostgreSQL, including client secrets and passwords. Restarting or recreating Keycloak preserves them. **Startup import skips an existing realm**, so editing JSON or changing the client secret in `.env` does not update that realm. For later configuration changes, edit the committed template for fresh installations and apply the matching changes to the existing realm through the supported Admin Console (realm settings → Login; Clients → `cartside-apisix` → Settings / Credentials). Preserve registered users; do not delete or overwrite the realm to apply settings. Set an existing client's secret explicitly in Credentials if changing `CARTSIDE_OIDC_CLIENT_SECRET`, then use the same value in future APISIX configuration. Admin bootstrap variables apply only when creating the initial administrator; change an existing administrator password through the console. The bootstrap administrator is temporary; create a permanent local administrator and remove the bootstrap account if keeping this environment long term.

For database password rotation, edit `.env`, rerun the provisioning job, and recreate Keycloak. This briefly interrupts its database access. Startup/environment changes require recreation; a plain restart retains the old container environment:

```sh
docker compose run --rm keycloak-db
docker compose up -d --force-recreate --wait keycloak
```

For ordinary persistence verification, run the Compose commands from the repository root. Compose starts infrastructure only. Before checking the four Go health URLs, keep the host-run Go server running in a separate terminal:

```sh
cd /path/to/cartside
set -a
. ./.env
set +a
cd backend
HTTP_ADDR=0.0.0.0:8080 go run ./cmd/api
```

Keep that terminal open; run the verification commands below in another terminal. For frontend browser checks, also keep Vite running using the frontend setup commands above. A refused connection on port 8080 together with gateway HTTP 502 means the Go upstream is not reachable; Keycloak health does not imply that Go is running.

```sh
docker compose config --quiet
docker compose ps -a
docker compose exec -T keycloak bash /opt/keycloak/healthcheck.sh
curl -fsS http://localhost:8180/realms/cartside/.well-known/openid-configuration
curl -fsS http://localhost:8180/realms/cartside/protocol/openid-connect/certs
# Register through the account console before restarting.
docker compose restart keycloak
docker compose up -d --wait keycloak
docker compose exec -T keycloak bash /opt/keycloak/healthcheck.sh
curl -fsS http://localhost:9080/api/health/live
curl -fsS http://localhost:9080/api/health/ready
curl -fsS http://localhost:8080/health/live
curl -fsS http://localhost:8080/health/ready
```

In the browser, open the account console, select Register on its sign-in page, complete the form, and confirm Personal info shows your account. After restarting Keycloak, use a fresh private window, sign in with the same credentials, and confirm the same account. Open http://localhost:9080 and a frontend fallback path such as http://localhost:9080/catalog; both should show Backend ready. Registration here creates only a Keycloak user; it creates no application account.

Container-network discovery and key verification (uses the existing APISIX image's HTTP client; prints only public URLs):

```sh
docker compose exec -T apisix resty -I /usr/local/apisix/deps/share/lua/5.1 -e '
local h = require("resty.http").new()
h:set_timeout(2000)
local r = assert(h:request_uri("http://keycloak:8080/realms/cartside/.well-known/openid-configuration"))
assert(r.status == 200)
local d = require("cjson").decode(r.body)
assert(d.issuer == "http://localhost:8180/realms/cartside")
print(d.issuer); print(d.authorization_endpoint); print(d.token_endpoint); print(d.jwks_uri)
assert(assert(h:request_uri(d.jwks_uri)).status == 200)
local t = assert(h:request_uri(d.token_endpoint, {
  method="POST", body="grant_type=client_credentials&client_id=cartside-apisix",
  headers={["Content-Type"]="application/x-www-form-urlencoded"}
}))
assert(t.status == 401) -- endpoint reachable; missing client credentials rejected
'
```

Official references pinned to the selected [Keycloak 26.8.1 container guide](https://github.com/keycloak/keycloak/blob/26.8.1/docs/guides/server/containers.adoc), [health guide](https://github.com/keycloak/keycloak/blob/26.8.1/docs/guides/observability/health.adoc), [hostname guide](https://github.com/keycloak/keycloak/blob/26.8.1/docs/guides/server/hostname.adoc), [realm import guide](https://github.com/keycloak/keycloak/blob/26.8.1/docs/guides/server/importExport.adoc), and [APISIX 3.19 OIDC plugin](https://apisix.apache.org/docs/apisix/3.19/plugins/openid-connect/).

### Container-to-host networking and exposure

APISIX connects to `host.docker.internal:8080`. Compose adds Docker's `host-gateway` mapping for Linux Docker Engine (20.10+). On native Linux this normally resolves to a Docker bridge address on the host. Go must listen on an interface reachable at that address: a loopback-only listener is insufficient. `HTTP_ADDR=0.0.0.0:8080` works across interfaces, or on native Linux you can bind only the relevant Docker bridge address if it is stable and reachable. Host firewall rules must allow traffic from the Docker network.

Docker Desktop normally supplies `host.docker.internal` through its host networking integration; the explicit host-gateway mapping must resolve to a reachable host address in your Desktop installation. Confirm with `docker compose exec apisix cat /etc/hosts` and the gateway requests above. Desktop runs containers inside a VM, so do not assume that a native Linux bridge address exists on the desktop host. If using a remote Docker context, this name refers to the Docker host, not necessarily the machine running Go. Use a local Docker context for this setup.

The gateway port is published only on `127.0.0.1:9080`. Binding Go to `0.0.0.0:8080` also exposes its direct listener on LAN interfaces, subject to the host firewall; use a trusted development machine and restrict inbound access. Gateway routes authenticate browser sessions, but this development topology does **not** prevent direct access to host Go or Vite, or enforce gateway-exclusive access. Go verifies access tokens independently and never trusts plain identity headers. Ports 5432, 8180, 9080, 8080, and 5173 must be available. Do not stop another checkout’s services without permission. A separate Compose project isolates volumes but does not isolate published ports; changing ports also requires coordinated gateway, Vite WebSocket, issuer, and realm redirect changes.

### Local monitoring alerts

The optional observability profile evaluates five local Prometheus alerts for expected API/APISIX scrape targets, sustained API 5xx ratio and p95 latency, and assistant provider/transport failures. These demo thresholds use traffic guards and pending durations; no notifications are sent. See [definitions, tests, states and limitations](docs/observability.md#local-alert-rules). Run `python scripts/verify-alerts.py` with the configuration Python dependencies and Docker installed. The isolated monitoring runner demonstrates pending/firing/recovery without touching developer services.

Demo thresholds: expected API/APISIX scrape failure or target absence for **1m**; per-route API 5xx ratio **>10%** or p95 **>1s**, each with **20 observations/completions over 5m**, sustained for **2m**; assistant provider/transport failure ratio **>20%**, with **20 eligible calls over 5m**, sustained for **2m**. Health/metrics probes, ordinary 4xx responses, cancellations and rate limits do not become provider outage signals. Prometheus evaluates rules; inspect `/alerts` and `/api/v1/rules` using the documented temporary loopback port override. Stopping optional monitoring stops evaluation; idle application traffic does not trigger scrape alerts.

### Gateway configuration and health

`infra/apisix/config.yaml` selects file-driven standalone mode (`deployment.role: data_plane`, `role_data_plane.config_provider: yaml`) and disables the Admin API and TLS listener. `infra/apisix/apisix.yaml` defines public health/landing routes, protected APIs/session check, and a protected frontend catch-all and must retain its final `#END` marker. Both files and the health probe are mounted read-only. There is no etcd, dashboard, or Admin API. The enabled OIDC plugin handles authentication; serverless functions sanitize unused incoming identity headers and serve minimal gateway responses.

The pinned image is `apache/apisix:3.19.0-ubuntu`. See [upgrade findings and rollback](docs/observability.md#apisix-319-upgrade) before recreating an existing gateway. Configuration follows the official APISIX 3.19 [deployment modes](https://apisix.apache.org/docs/apisix/3.19/deployment-modes/), [proxy-rewrite](https://apisix.apache.org/docs/apisix/3.19/plugins/proxy-rewrite/), [upstream timeout schema](https://apisix.apache.org/docs/apisix/3.19/admin-api/#upstream), and the [3.19.0 Control API documentation](https://github.com/apache/apisix/blob/3.19.0/docs/en/latest/control-api.md). Connection timeout is 1 second; send and read inactivity timeouts are 5 seconds each. Retries are disabled. These bound stalled upstream operations, rather than setting a total streaming-response deadline.

Upgrade findings: official 3.15–3.19 release notes were reviewed. File-driven standalone mode and existing plugin priorities are preserved. OIDC session options now use the flat lua-resty-session 4 fields and a one-hour absolute timeout; existing cookies may need fresh sign-in. The new default AI FFI transport passed the isolated fault/no-retry checks. WebSocket metric labels and sparse histogram export behavior are documented; overview queries remain compatible.

To roll back, restore the saved 3.14.1 gateway YAML/Lua files and `apache/apisix:3.14.1-debian` image pin together, validate Compose, then run `docker compose up -d --no-deps --force-recreate --wait apisix` and `docker compose exec -T apisix apisix test`. Preserve `.env`, Keycloak users and all volumes; never use `down -v`. See the linked rollback procedure for the required configuration snapshot and cookie limitations.

The image includes `resty` and its HTTP client library (verified); the probe does not depend on `curl` or `wget`. The Compose health check uses `resty` with a two-second HTTP timeout to request `/v1/healthcheck` on the container-only control listener, `127.0.0.1:9090`. The probe checks that the control endpoint returns 200; it does not interpret upstream health or call Go. Neither control port 9090 nor Admin port 9180 is published. APISIX can remain healthy when Go is stopped or PostgreSQL is unavailable. Application readiness is checked separately through `/api/health/ready`.

The frontend route enables WebSocket upgrades; see the [Vite server options](https://vite.dev/config/server-options) and [APISIX route schema](https://apisix.apache.org/docs/apisix/3.19/admin-api/#route). APISIX receives only the OIDC client secret and separate session secret through its environment, not database credentials or the `.env` file. Its committed YAML uses fixed local ports and an upstream address; Compose does not interpolate bind-mounted file contents. Changing `HTTP_ADDR` changes Go only: if changing its port, also update the upstream in `infra/apisix/apisix.yaml`. After configuration changes, use `docker compose up -d --force-recreate --wait apisix` so both startup settings and newly replaced bind-mounted files are loaded.

### Outage and configuration verification

```sh
docker compose config --quiet
docker compose exec -T apisix apisix test
docker compose exec -T apisix sh -c 'command -v resty'
docker compose stop postgres
curl -i http://127.0.0.1:9080/api/health/live
curl -i http://127.0.0.1:9080/api/health/ready
docker compose ps apisix
docker compose up -d --wait postgres
curl -i http://127.0.0.1:9080/api/health/ready
```

While PostgreSQL is stopped, liveness stays 200 and readiness returns HTTP 503 with `{"status":"unavailable"}` through APISIX. Each readiness request pings through the shared pool with a one-second context timeout; subsequent requests recover after PostgreSQL returns, without restarting Go or APISIX.

Stop Go with Ctrl+C: `/api/health/live` then returns a gateway error (normally 502 for connection refusal, 504 for an upstream timeout), while APISIX stays healthy. Restart Go using the setup command and verify both gateway health routes return 200 again. SIGINT or SIGTERM gives Go up to 10 seconds for active HTTP requests, followed by pool closure.

PostgreSQL data persists in the Compose named volume across stops and container recreation. Use `docker compose stop` to stop infrastructure without deleting data. Do not use `docker compose down -v` unless intentionally deleting data. PostgreSQL initialization variables apply only to an empty volume; changing credentials in `.env` does not change an existing database user's password.

Backend validation commands (run from `backend/`):

```sh
gofmt -w cmd internal
go vet ./...
go test ./...
go build ./...
```

Browser verification (with all three processes running):

- Open http://localhost:9080 and confirm “Backend ready”; inspect assets and the HMR WebSocket in browser developer tools: all must use port 9080.
- Edit visible text in `frontend/src/App.tsx`; confirm it changes without a document reload and without a direct connection to port 5173, then restore the edit.
- Stop PostgreSQL using `docker compose stop postgres`, click Retry, and confirm “Backend unavailable”. Liveness and the independent APISIX health check must still pass.
- Restore PostgreSQL with `docker compose up -d --wait postgres`, click Retry, and confirm “Backend ready”. No volume deletion or Go restart is required.
- In browser developer tools, delay the readiness request to observe loading, or block `/api/health/ready` and click Retry to observe “Network error”. Unblock and retry to recover.

The gateway authentication procedure and remaining security boundaries are documented below.

## Health checks

- **Go liveness (implemented):** `GET /health/live` confirms the process can respond.
- **Go readiness (implemented):** `GET /health/ready` checks PostgreSQL reachability with a bounded ping.
- **Infrastructure health:** APISIX control, PostgreSQL, and Keycloak readiness checks are implemented.
- **Routing check (implemented):** `/api/health/live` and `/api/health/ready` reach Go through APISIX.
- **Authentication check (implemented):** `/auth/session` verifies the gateway session without calling Go; unauthenticated non-health APIs return 401. Go verifies identity separately at `/api/me`.

Routine health probes do not call a paid AI provider. Healthy individual services do not replace checks of routing and authentication across components.

## CI and delivery

`.github/workflows/ci.yml` runs on pull requests, pushes to `main`, and manual dispatch. Independent **Backend**, **Frontend**, and **Configuration** jobs use read-only repository permissions and cancel superseded runs for the same PR or branch. Official actions are pinned to full commit SHAs with release comments; update both together.

- Backend: non-mutating formatting check, vet, race tests, and build. Go comes from `backend/go.mod`; dependencies are cached against `backend/go.sum`. Tests use local fixtures and need no PostgreSQL, Keycloak, credentials, or repository secrets.
- Frontend: Node comes from `frontend/package.json`; npm caches against `frontend/package-lock.json`. Runs `npm ci`, lint, typecheck, Vitest frontend tests (`npm test`), and production build.
- Configuration: Python 3.13 and pinned PyYAML parse tracked JSON/YAML; Compose validates with disposable values for every required variable and `config --quiet`. No rendered credentials are printed. Parsing does not prove runtime APISIX schemas, routing, or OIDC behavior; CI does not start the identity stack.

GitHub-hosted Ubuntu runners provide Docker Compose and the C compiler needed for race tests. Local checks require the Go/Node versions above, Python 3 (install `scripts/requirements-ci.txt` in a virtual environment for configuration parsing), and Docker Engine/Desktop with Compose v2 and a local context. GitHub Actions must be enabled in repository settings; no repository secrets are needed. Confirm all three jobs after pushing; a local pass is not a remote CI pass. Deployment and artifact publishing remain future work.

## Phase 1: foundation

Implementation order:

1. Create the repository scaffold and configuration template.
2. Establish local infrastructure with Docker Compose.
3. Add Go liveness and PostgreSQL readiness checks.
4. Route the frontend and backend through APISIX.
5. Configure Keycloak registration and APISIX-managed sign-in.
6. Add a protected current-user endpoint and verify authentication boundaries.
7. Add CI and verify the documented setup from a fresh clone.

Exit criteria:

- [x] Go runs locally with a configurable listen address and graceful shutdown.
- [x] Go liveness responds directly with HTTP 200 and the expected JSON, without authentication or external dependency checks.
- [x] Backend formatting, vet, test, and build checks pass.
- [x] The README contains verified backend run and liveness verification commands.
- [x] Local PostgreSQL and Go configuration is documented in the committed template.
- [x] Local PostgreSQL, APISIX, and Keycloak infrastructure is configured with Docker Compose.
- [x] React and Go run locally behind APISIX.
- [x] Go liveness and readiness checks work through the gateway.
- [x] A customer can register and sign in through Keycloak’s account console and the APISIX-managed application flow.
- [x] An authenticated request reaches a protected Go endpoint and resolves the user's identity.
- [x] An unauthenticated non-health API request is rejected.
- [x] Initial CI checks pass.
- [x] Full setup works from a fresh checkout with new secrets and isolated volumes, including registration, sign-in, `/api/me`, logout, and persistence after restart.

### Fresh-checkout verification (2026-10-15)

A separate local clone with the pending CI/documentation changes overlaid used newly generated secrets and Compose project `cartside-phase1-ue8j6g0u`, with its own data/socket volumes. Automated checks passed: Go formatting, vet, unit/race tests and build; Node 24 `npm ci`, lint, typecheck and build; actionlint; tracked JSON/YAML parsing with PyYAML 6.0.3, including rejection of malformed fixtures; and quiet Compose validation with both new secrets and disposable CI values.

All five documented ports were occupied by the developer’s running services. Those services, configuration, users, and volumes were untouched. Supplemental runtime checks used a temporary override: PostgreSQL published an ephemeral loopback port; APISIX and Keycloak published no ports. Go and Vite used free loopback ports. Infrastructure became healthy; APISIX’s configuration test and independent health probe passed; direct Go liveness/readiness returned 200, unauthenticated `/me` returned 401, and Vite served HTTP 200. The imported realm retained its database ID across PostgreSQL/Keycloak restart. This verifies realm persistence, not customer sign-in persistence. Only temporary processes, containers, network, volumes, secrets, and checkout were removed.

**No browser checks were performed in this fresh checkout.** The prior README recorded Chrome registration/sign-in, identity display and `/api/me`, header-forgery rejection, HMR, and logout against initialized volumes. Those earlier checks underpin the checked authentication criteria above; they do not establish fresh-install success. Exact README runtime setup, gateway-to-host routing, registration, authenticated `/api/me`, logout, and customer persistence remain unverified in isolation because of fixed-address conflicts. GitHub Actions Backend, Frontend, and Configuration jobs passed on commit `bc931a2` in the [manually dispatched run](https://github.com/WilliamSchweitzer/cartside/actions/runs/38826000326). The host lacked `python3-venv`/pip, so the local parser check used the pinned PyYAML wheel extracted into the temporary directory; CI installs it normally with setup-python/pip.

Remaining manual checklist, on a machine where the documented ports are free:

- [x] Clone separately; select a unique Compose project (`export COMPOSE_PROJECT_NAME=cartside-fresh-check`), copy the template, and follow Setup instructions with new secrets. Never reuse another checkout’s `.env` or volumes.
- [x] Confirm all infrastructure is healthy; start Go and Vite exactly as documented. Verify direct and gateway health routes, APISIX’s independent probe, and signed-out API/session 401 responses.
- [x] In a private browser, register and sign in through the gateway. Confirm Backend ready, protected assets/HMR through 9080, `/auth/session`, and minimal no-store `/api/me` matching the displayed identity.
- [x] Restart this project’s PostgreSQL and Keycloak, then sign in in a fresh private window with the same customer; confirm the same subject and working readiness.
- [x] Sign out, confirm the stable landing page and API/session 401 responses, then confirm a new login requires credentials.
- [x] Stop only this checkout’s host processes. From this disposable checkout, remove only its resources with `docker compose -p cartside-fresh-check down -v`, then remove its temporary secrets/checkout.

## Later milestones

- Add AI suggestions and explicit customer acceptance.
- Build the React review screen using the implemented authenticated checkout API.
- Prepare deployment and managed secret delivery.

Building a custom Go OIDC provider is a potential separate learning milestone after the application works with Keycloak.

## Gateway authentication (implemented)

Use **http://localhost:9080** consistently. APISIX 3.19.0 uses the existing confidential `cartside-apisix` client with Authorization Code, PKCE, state, and nonce. It owns encrypted, signed HttpOnly browser cookies and all tokens; React has no OIDC library or token storage. Never open Vite directly to test authentication.

For an existing checkout, run the secret-generation block above: it now adds `CARTSIDE_SESSION_SECRET` without replacing existing credentials. Generate each secret independently with Python `secrets.token_hex(32)` (32 random bytes, encoded as hex). Do not rotate the existing client secret just to add the session secret. Compose requires both secrets and passes them only to APISIX; `nginx_config.envs` explicitly preserves them in Nginx workers. Plugin fields use supported `$ENV://CARTSIDE_OIDC_CLIENT_SECRET` and `$ENV://CARTSIDE_SESSION_SECRET` references. Mounted YAML is not Compose interpolation. Changing a session secret invalidates existing gateway cookies.

**For the gateway-session setup, existing realms require the logout setting below:** startup import skips them. The backend identity milestone also requires the audience reconciliation described below. In the local Keycloak Admin Console, select realm `cartside`, Clients → `cartside-apisix` → Settings → Logout settings. Set Valid post logout redirect URIs to exactly `http://localhost:9080/signed-out` and save. Keep the login redirect exactly `http://localhost:9080/auth/callback`, the existing client secret, and all users. The committed realm template already has these settings for new installations. Do not delete/import over an initialized realm or delete database volumes. Then run:

```sh
docker compose config --quiet
docker compose up -d --force-recreate --wait apisix
docker compose exec -T apisix apisix test
```

| Request | Gateway behavior |
| --- | --- |
| `/`, frontend paths, assets, HMR | Session required; signed-out navigation redirects to Keycloak |
| `/auth/callback` | OIDC plugin checks state/nonce, exchanges the code internally, validates the ID token and issuer, saves the session, then redirects to the original app path |
| `/auth/session` | Session required; returns only `{"authenticated":true}` with no-store, or 401; no upstream |
| `/api/health/live`, `/api/health/ready` | Exact public exceptions, forwarded to Go with `/api` stripped |
| `/api`, other `/api/*` | Cookie session required; 401 without a session, otherwise forwarded with `/api` stripped and query preserved |
| `/auth/logout` | Clears the gateway cookie and redirects to Keycloak's discovered end-session endpoint with ID-token hint and a fixed approved post-logout URL |
| `/signed-out` | Public gateway HTML, no-store, with an explicit sign-in link; no Vite dependency or automatic login |

Protected routes share identical session secret, cookie path `/`, cookie flags and lifetime. API routes use `bearer_only: false` with `unauth_action: deny`, so the browser cookie works without redirecting fetches to HTML. Incoming Authorization and common token/identity headers are cleared before authentication; identity output headers are disabled; only backend routes forward the authenticated session access token as X-Access-Token. Cookies are removed before upstream forwarding, after authentication. Go verifies that token independently and exposes minimal verified identity at `/api/me`. Only exact health paths bypass authentication; unknown API paths return Go's 404 after login.

Keycloak discovery is fetched at `http://keycloak:8080/realms/cartside/.well-known/openid-configuration`. Hostname v2 keeps issuer and browser authorization/logout URLs at `http://localhost:8180`, while dynamic backchannel discovery advertises internal token and key URLs. Issuer validation remains enabled. No container needs to reach the host's `localhost:8180`.

### Session and development security limits

Cookies use HttpOnly, SameSite=Lax (compatible with top-level Code callbacks), no Domain, and a 3600-second lifetime. The pinned lua-resty-session 4.1.5 uses flat cookie options and `absolute_timeout: 3600`; the one-hour bound is now an absolute session lifetime. Existing cookies may require signing in again after the upgrade. Keycloak session/token expiration also applies. Local HTTP requires `cookie_secure: false`; deployment requires HTTPS, `cookie_secure: true`, approved HTTPS issuer/callback/logout URLs, TLS verification, restricted upstream network access, and a deliberate absolute/idle session policy. This milestone supplies no production deployment or access isolation.

APISIX uses an explicit safe JSON access format that excludes callback query strings; see [observability](docs/observability.md). Error logging is restricted to `crit` to avoid OIDC error messages containing sensitive provider responses. Do not enable debug/verbose HTTP tracing, capture cookies, or publish HAR files containing codes/tokens. Local Docker administrators can inspect environment secrets. The encrypted cookie contains tokens managed by APISIX; HttpOnly prevents frontend JavaScript access. Cookie deletion and Keycloak logout end the browser sessions; stateless gateway cookies do not provide a server-side revocation list for a stolen cookie. Central revocation/session storage is a deployment consideration.

Logout always uses the fixed local post-logout URL; query parameters do not select a redirect destination. Keycloak validates its exact redirect allowlist. The public landing page does not automatically start login. Logout is a navigation GET supported by the pinned OIDC library; SameSite=Lax does not itself prevent forced logout.

**Authenticated cart mutations enforce CSRF protection** with a mandatory custom header, no cross-origin CORS permission, and rejection of cross-site Fetch Metadata, as documented below. Go also validates inputs and scopes ownership to the verified principal. SameSite cookies alone are insufficient. State/nonce protect the login flow, not application mutations. Host ports 8080 and 5173 remain reachable directly, bypassing the gateway. Restrict host firewall access in development; establish network isolation before claiming gateway-exclusive access; Go already validates forwarded tokens independently.

### Authentication verification

Start Go and Vite as described above. Without cookies:

```sh
# 302 to Keycloak (do not publish Location headers from a live login):
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:9080/
# Each must return 401, without following redirects:
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:9080/auth/session
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:9080/api/nonexistent
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:9080/api
# 200 when Go/PostgreSQL are ready; landing is independent:
curl -fsS http://localhost:9080/api/health/live
curl -fsS http://localhost:9080/api/health/ready
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:9080/signed-out
```

In a fresh private browser:

1. Visit the gateway; confirm a Keycloak login page. Register or sign in and confirm return to the app, protected assets, and “Backend ready”.
2. Fetch `/auth/session` from that tab with same-origin credentials: expect 200 and only `{"authenticated":true}`. Fetch `/api/nonexistent`: expect Go's 404, proving the cookie passes API enforcement. A fresh cookie-free browser must receive 401 for both.
3. Click Retry readiness check. Make a temporary visible text edit in `frontend/src/App.tsx`, confirm HMR without document reload and the WebSocket through port 9080, then restore it.
4. Click Sign out; confirm Keycloak ends SSO and returns to `/signed-out`. Reload: it must stay there. `/auth/session` and non-health APIs must now return 401. Click Sign in: credentials must be required again.

Version-matched implementation references: [APISIX OIDC plugin 3.19.0](https://github.com/apache/apisix/blob/3.19.0/apisix/plugins/openid-connect.lua), [secret resolution](https://github.com/apache/apisix/blob/3.19.0/apisix/secret.lua), [environment references](https://github.com/apache/apisix/blob/3.19.0/apisix/core/env.lua), [Nginx environment template](https://github.com/apache/apisix/blob/3.19.0/apisix/cli/ngx_tpl.lua), [pinned dependencies](https://github.com/apache/apisix/blob/3.19.0/apisix-master-0.rockspec), [lua-resty-openidc 1.9.0](https://github.com/zmartzone/lua-resty-openidc/blob/v1.9.0/lib/resty/openidc.lua), [lua-resty-session 4.1.5](https://github.com/bungle/lua-resty-session/blob/v4.1.5/lib/resty/session.lua), [Keycloak hostname 26.8.1](https://github.com/keycloak/keycloak/blob/26.8.1/docs/guides/server/hostname.adoc), and [Keycloak logout validation 26.8.1](https://github.com/keycloak/keycloak/blob/26.8.1/services/src/main/java/org/keycloak/protocol/oidc/endpoints/LogoutEndpoint.java).

## Verified backend identity

Implemented in this milestone: Go independently validates the session access token and serves `GET /me` (`GET /api/me` through APISIX). The response is only `{"subject":"…","issuer":"…"}` with `Cache-Control: no-store`. Missing or invalid credentials produce generic JSON 401 responses. Every Go route except exact GET liveness/readiness paths passes through authentication, including unknown routes and unsupported methods. React uses a relative `/api/me` fetch and displays loading, identity, renewal-required/401, and service-failure states. Renewal is explicit navigation through logout and the signed-out sign-in link; fetches never trigger a redirect loop.

### Configuration and existing realms

Add these values to an existing ignored `.env` before starting the new Go binary (they are already in the template for new checkouts):

```sh
OIDC_ISSUER=http://localhost:8180/realms/cartside
OIDC_AUDIENCE=cartside-api
OIDC_JWKS_URL=http://localhost:8180/realms/cartside/protocol/openid-connect/certs
```

All three are required at Go startup. The issuer is the exact token identity, including scheme/host/path; the independently configured JWKS URL is a reachable transport endpoint. Host-run Go reaches Keycloak via localhost:8180; APISIX still uses Compose DNS. Neither issuer nor audience validation is disabled. Configuration validation performs no network calls. Liveness remains process-only and readiness remains PostgreSQL-only, including during a Keycloak outage.

Fresh imports define a bearer-only `cartside-api` client and an `oidc-audience-mapper` on `cartside-apisix`, with `included.client.audience=cartside-api`, access-token inclusion enabled and ID-token inclusion disabled. This explicit mapper adds the backend audience without requiring a browser-requested optional scope. The API client has no browser login, password grant, or service-account flow.

Startup import does not modify an existing realm. Apply the same configuration with the committed, idempotent Admin REST reconciliation script, using the existing local administrator credentials:

```sh
# From the repository root; source only your trusted local file.
set -a
. ./.env
set +a
python3 infra/keycloak/reconcile-audience.py
# Safe to repeat. Does not delete realms, users, or volumes, or rotate secrets.
docker compose config --quiet
docker compose up -d --force-recreate --wait apisix
docker compose exec -T apisix apisix test
# Restart the host Go process with the exported verification settings.
```

The script reads the committed realm template, creates/updates only the API client and named gateway audience mapper, and suppresses credentials, tokens, and provider error bodies. It requires Python 3 and a reachable local Keycloak administrator API. Existing access tokens retain their original claims until replaced. **Sign out and sign in again after applying the mapper** to obtain a token with the new audience; an old session can pass `/auth/session` while `/api/me` correctly rejects its old token.

### Trust boundary and verification policy

In APISIX's rewrite phase, `serverless-pre-function` (priority 10000) clears client Authorization, X-Access-Token, X-ID-Token, X-Refresh-Token, X-Userinfo, X-User-ID and X-Forwarded-User headers. `openid-connect` (2599) then authenticates the browser cookie and sets **`X-Access-Token`**, containing only the authenticated session's raw access token, on backend routes. `proxy-rewrite` (1008) removes Cookie and rewrites the path; it preserves this newly generated token. Session-check responses run in `serverless-post-function` (-2000). These priorities were checked in the pinned running image. Frontend and session-check routes keep token output disabled. Header names are case-insensitive. Go consumes only X-Access-Token; it ignores Authorization and all plain identity headers. No upstream echoes credentials, and gateway access logging remains disabled with error logging restricted to `crit`.

Go uses `github.com/coreos/go-oidc/v3` v3.14.1 with an explicit RS256 allowlist, signature verification, exact issuer and backend audience checks, required unexpired expiration and nonempty subject, and validation of `nbf` and `iat` when present. No positive clock-skew allowance is applied by the application; keep clocks synchronized. The reusable remote JWKS set caches keys and fetches again when the signing key changes. HTTP key retrieval has a three-second client timeout, redirects disabled, and verification has a four-second request deadline. A known cached key can verify during an outage; an unverifiable token fails closed. Verification errors and token values are never logged or returned. Only the verified subject and issuer enter the request context.

The durable external identity is **(issuer, subject)**, never email. There is no application user table; catalog schema migrations are separate from identity. Gateway authentication establishes a browser session; Go token validation establishes a cryptographically verified caller. The cart module now scopes all operations to this verified principal; checkout scopes orders to that same verified identity; order browsing and its HTTP authorization remain planned. Cart HTTP mutations now require the CSRF header documented below.

Local HTTP, insecure development cookie settings, and reachable host ports remain limitations. **A valid access token can authenticate directly to Go using X-Access-Token.** Network isolation and gateway-exclusive access are not implemented. Direct Vite also remains reachable. Tokens, session cookies and administrator credentials need trusted HTTPS transport and appropriate network controls in deployment. Local JWT validation does not immediately revoke an already issued access token on browser logout; it remains usable until expiration unless a future revocation policy is added. React receives no raw tokens and never stores them; the browser carries APISIX's encrypted HttpOnly session cookie.

### Verification

Unit tests generate local RSA keys and serve local JWKS fixtures; they require no Keycloak. They cover absent/malformed/duplicate tokens, forged plain identity headers, invalid signature, a disallowed algorithm, wrong/missing issuer or audience, expired/missing expiration, missing/empty subject, future/malformed time claims, successful minimal identity, cached keys during outage, key rotation, unavailable keys, protected unknown routes/methods, public health, and invalid startup configuration.

```sh
cd backend
gofmt -w cmd internal
go vet ./...
go test ./...
go test -race ./...
go build ./...
cd ../frontend
# Use Node 24 (the project's pinned runtime).
npm run lint
npm run typecheck
npm test
npm run build
cd ..
docker compose config --quiet
docker compose exec -T apisix apisix test
# Signed out: all must return 401, without following redirects.
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:9080/api/me
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:9080/auth/session
curl -s -o /dev/null -w '%{http_code}\n' -H 'X-User-ID: forged' -H 'X-Forwarded-User: forged' http://localhost:8080/me
curl -s -o /dev/null -w '%{http_code}\n' -H 'X-Access-Token: fake' http://localhost:8080/me
```

In a signed-in browser, fetch `/api/me` with same-origin credentials and check the minimal subject/issuer JSON and no-store header. Repeat with fake Authorization, X-Access-Token and identity headers: the result must remain the authenticated session's identity. Do not print/copy cookies or real tokens. Check `/auth/session`, readiness Retry, logout, and the signed-out landing page as described above. To inspect the identity UI, delay `/api/me`, simulate a 503 then Retry, and simulate a 401; the latter must show explicit return-to-sign-in navigation without automatic redirects.

Version-matched official references: [APISIX 3.19.0 OIDC forwarding](https://github.com/apache/apisix/blob/3.19.0/apisix/plugins/openid-connect.lua), [pre-function priority](https://github.com/apache/apisix/blob/3.19.0/apisix/plugins/serverless-pre-function.lua), [proxy-rewrite priority and phase](https://github.com/apache/apisix/blob/3.19.0/apisix/plugins/proxy-rewrite.lua), [Keycloak 26.8.1 audience mapper](https://github.com/keycloak/keycloak/blob/26.8.1/services/src/main/java/org/keycloak/protocol/oidc/mappers/AudienceProtocolMapper.java), [Keycloak 26.8.1 Admin REST API](https://www.keycloak.org/docs-api/26.8.1/rest-api/index.html), [go-oidc v3.14.1 verifier](https://github.com/coreos/go-oidc/blob/v3.14.1/oidc/verify.go), and [cached JWKS and rotation](https://github.com/coreos/go-oidc/blob/v3.14.1/oidc/jwks.go).


## Phase 2 milestone 1: catalog persistence (implemented)

`backend/migrations/00001_catalog.sql` is a reversible Goose v3 SQL migration.
The Go dependency pins Goose to **v3.24.3**; the dedicated `cmd/migrate` command
uses that library, so no separately installed Goose binary is required. SQL files
are embedded in the command. Goose records applied versions in
`goose_db_version` in the configured application database. A second `up` applies
only pending migrations. Each SQL migration runs transactionally by default.

From the repository root, export the existing trusted local configuration, then
run these commands from **`backend/`**:

```sh
set -a
. ./.env
set +a
cd backend
go run ./cmd/migrate up
go run ./cmd/seed
```

Both commands use the existing `DATABASE_URL` convention and require no OIDC
settings. They do not load `.env` themselves. Neither runs during API startup.
Run migrations explicitly before seeding. Seed output reports `inserted` and
`skipped` counts without connection details. The seed command is for development
sample data only; point it only at the intended development application database.
There is no reset command.

The `products` table has UUID keys (PostgreSQL's built-in `gen_random_uuid()`
default, no extension), required nonblank descriptive fields, six simple category
values, integer nonnegative cents, explicit USD currency, and non-null availability
defaulting to true. The readable fixture is
`backend/internal/catalogseed/products.json`: exactly 100 fictional groceries,
with fixed packages, prices, and committed deterministic UUIDv5 IDs derived from
`https://cartside.example/sample-products/` plus each original product name.
All begin available. `/images/products/placeholder.svg` is the stable local image
reference, now supplied by the catalog UI in `frontend/public/images/products/`.

The full fixture is validated before any write, including count, UUID uniqueness,
required fields, category, USD currency, availability, and PostgreSQL integer price
bounds. All inserts use one transaction and `ON CONFLICT (id) DO NOTHING`.
Existing rows, including price and availability edits, are preserved; nonfixture
rows are untouched. **Changing the fixture does not update an already-seeded
record.** Only primary-key conflicts are skipped; other constraint failures abort
and roll back the seed.

`go run ./cmd/migrate down` (from `backend/`) rolls back **one** migration.
At version 3 this deletes all carts and cart items; the version 2 rollback retains
the shared pg_trgm extension; rolling back version 1 deletes all catalog data,
including custom products and edits. Do not run rollback against existing developer
data. Rollback and reapply verification must use a disposable database.

Default unit tests need no database. Live database checks are explicitly separated
with the `integration` build tag. They require an **empty, disposable database**
and refuse any database already containing user tables. The test migrates, seeds,
edits data, drops/recreates the catalog, and tests an injected constraint failure.
It leaves test data behind; discard only that disposable database afterward.
From `backend/`, with a separately provisioned empty PostgreSQL 17 database:

```sh
# Set this to your disposable database URL, never the application DATABASE_URL.
export CARTSIDE_TEST_DATABASE_URL='postgres://catalog_test:replace_with_test_password@127.0.0.1:55432/catalog_test?sslmode=disable'
go test -tags=integration -race ./internal/catalogseed -run TestCatalogLifecycle -count=1 -v
```

CI runs this explicit integration command against a fresh PostgreSQL service,
separately from default unit tests. It verifies empty migration and no-op rerun,
100 first inserts, duplicate-free reseeding, preserved edits and nonfixture data,
atomic seed failure, and disposable-only rollback/reapply. No catalog endpoints,
search, cart behavior, AI, or frontend features are included in this milestone.

Local milestone verification passed against a separate PostgreSQL 17.10 container:
the full integration test with the race detector and command smoke checks (`up`
twice, seed twice, disposable `down`/`up`). Formatting, `go vet ./...`,
`go test ./...`, `go test -race ./...`, `go build ./...`, and configuration parsing
also passed. Existing development databases, volumes, and Keycloak data were not
modified. The updated GitHub Actions workflow has not yet been run remotely.


## Phase 2 milestone 2: read-only catalog API (implemented)

Both endpoints require the existing APISIX browser session and independently
verified Go authentication. APISIX already forwards these routes and preserves
query strings; it removes `/api`, so Go handles `/products` and `/products/{id}`.
All catalog responses use JSON and `Cache-Control: no-store`.

- `GET /api/products?q=apple&limit=20&offset=0` lists or searches products.
  Trimmed empty `q` lists the catalog. Search preserves literal substrings in name
  or description, case-insensitively, and adds typo-tolerant candidates for queries
  of at least three characters. `%`, `_`, and backslashes stay strictly literal.
  Internal Unicode whitespace runs collapse to one space after validation.
  Search accepts at most 200 Unicode characters after trimming.
- `limit` defaults to 20 and accepts 1–100. `offset` defaults to 0 and accepts
  0–2147483647. Numeric values must contain decimal digits only. Empty numeric
  values, signs, fractions, overflow, repeated recognized parameters, malformed
  query encoding, invalid UTF-8, NUL search characters, and overlong search text
  return 400. Unknown query parameters are ignored.
- Results include unavailable products. Empty search orders by name then UUID;
  search orders exact names, name prefixes, other literal names, then remaining
  candidates using name similarity plus 20% description similarity. Name and UUID
  break ties. Filtering and ranking precede pagination.
  The response is `{"items":[...],"limit":20,"offset":0,"has_more":false}`.
  No matches produce `"items":[]`. One extra row determines `has_more`;
  no total-count query runs.
- `GET /api/products/{id}` returns one product. IDs use the hyphenated
  36-character UUID syntax (hexadecimal is case-insensitive).
  Malformed IDs return 400; valid absent IDs return 404.
- Both representations contain `id`, `name`, `description`, `category`,
  `package_label`, `image_reference`, `price_cents`, `currency`, and
  `available`. Prices remain integer cents, including in JSON.
  Database failures return generic JSON 503 errors, distinct from missing products.
  Database error details and credentials are never returned.

The HTTP handlers validate input and call the injected catalog repository.
The repository uses the shared pgx pool, parameterized SQL, and a three-second
deadline per operation. PostgreSQL performs literal substring matching with
`strpos(lower(...), lower($1))` alongside explicit pg_trgm word-similarity
comparisons with an inclusive **0.44** cutoff. No connection similarity settings
change. New Goose migration `00002_catalog_search.sql` enables pg_trgm; run
`go run ./cmd/migrate up` before restarting the updated API. Its rollback retains
the database-wide extension to protect pre-existing/shared usage. No new index or
search service was added. See [search evaluation and limitations](docs/catalog-search.md)
for fixture cases, threshold measurements, query-plan evidence, reproducible
integration commands, and authenticated browser verification. Some short-word
typos still fail; this is similarity search rather than a spelling guarantee.

Run migrations and the development seed explicitly as documented above, then
restart Go with the existing configuration. In a signed-in tab at
http://localhost:9080, these browser-console examples use the existing HttpOnly
session without embedding credentials:

```js
await fetch('/api/products?q=apple&limit=2&offset=0', {
  credentials: 'same-origin'
}).then(r => r.json());

await fetch('/api/products/bb5ce88f-908c-5a99-8ae7-90b60b00ae1b', {
  credentials: 'same-origin'
}).then(r => r.json());
```

The detail example is the seeded Gala Apples UUID; existing developer edits
remain authoritative. Check pagination, an unmatched query, and a literal
percent query (`?q=%25`). Confirm `/api/me`, `/auth/session`, readiness, and
logout still work. After logout, catalog requests must return 401.

Verification from `backend/`:

```sh
gofmt -l cmd internal
go vet ./...
go test ./...
go test -race ./...
go build ./...
# Explicit integration run: a NEW empty disposable database, never DATABASE_URL.
# Set CARTSIDE_TEST_DATABASE_URL privately in your shell first.
go test -tags=integration -race ./internal/catalog -run TestPostgresCatalog -count=1 -v
```

The catalog integration test refuses a database containing user tables, applies
the real migration, and inserts isolated test products. It covers name and
description matching, case-insensitivity, literal wildcards/backslashes,
deterministic tie ordering, pagination and lookahead, empty results, unavailable
products, detail consistency, not-found, and database failure. It leaves data in
that disposable database. Use a separate fresh database for the earlier
`catalogseed` lifecycle test; do not run both against the same database.
Default unit tests require no database.

Local verification passed formatting, vet, unit tests, race tests, build, and
the explicit repository integration suite against a disposable PostgreSQL
17.10 container. Compose validation and the running APISIX configuration test
passed. Live signed-out gateway requests returned 401 for both catalog endpoints,
`/api/me`, and `/auth/session`; health routes and the signed-out page returned
200. Authenticated seeded-product requests through APISIX and browser
session/logout regression checks were not run: this session had no browser
automation capability or authenticated browser session, and the existing host
server was left running unchanged. Run the signed-in examples after restarting
Go. Existing developer records and persistent volumes were not modified.
No catalog UI, mutations, carts, AI integration, or deployment is included.


## Phase 2 milestone 3: read-only storefront (implemented)

The signed-in home page is a responsive grocery catalog with product name,
package, category, USD price (integer cents formatted with `Intl.NumberFormat`),
and explicit availability. Unavailable products remain visible. The seed's local
placeholder SVG is included; missing, broken, or nonlocal image references use a
local fallback. There are no remote image services or chat controls. The subsequent cart UI milestone adds cart controls.

Submit the labeled search form with Search or Enter. Clear resets the input and
browses from the first page. Empty search browses all groceries. Each request uses
20 products; Previous/Next use the submitted query, offset, and `has_more`, without
claiming a total count. Editing the input does not change the current results or
pagination. Submitting a changed query resets to the first page.

Catalog requests use the existing authenticated relative `/api/products` route,
same-origin credentials, and an eight-second timeout. Superseded requests are
aborted and ignored. Loading, empty results, service/network failure with Retry,
and 401 session renewal are explicit states. A 401 offers Return to sign-in
through the existing logout flow; it does not automatically redirect. The compact
identity area and logout remain in the header. Expand Service diagnostics for
backend readiness and its independent Retry action.

Run the existing frontend lint, typecheck, and build scripts using Node 24.
For browser verification after migrations, seed, and API startup:

- Sign in through http://localhost:9080 and browse multiple pages. Search by Enter
  and Search; edit the input without submitting, then change pages to confirm the
  submitted query stays active. Submit a new query from a later page.
- Try an unmatched search, Clear, and a literal `%` search. Confirm prices and
  packages against the API. Developer-edited unavailable products remain visible.
- Simulate failed image loads, catalog 503/network failures, delayed responses,
  and 401 responses in browser tooling. Retry should keep the submitted query and
  page; stale responses must never replace newer results. A stalled request should
  fail after eight seconds.
- Check narrow and wide layouts, keyboard submission and navigation, diagnostics,
  identity, and logout. After logout, `/api/products`, `/api/me`, and
  `/auth/session` must return 401.

Local verification passed Node 24 lint, typecheck, and production build, plus a
headless Chrome walkthrough through the running gateway. Controlled catalog
responses exercised search/Enter, draft-versus-submitted pagination, stale-request
cancellation, USD formatting, unavailable products, broken-image fallback, empty
results/Clear, 503/network/timeout failures and Retry, and 401 navigation. Layouts
at 375px and 1280px had no horizontal overflow. Identity, readiness Retry, logout,
and signed-out gateway protection used the real running services. A subsequent real-service check found an older running Go binary and an
unmigrated development database. After rebuilding/restarting Go and running the
existing migration and seed commands, authenticated catalog requests returned
200 with 20 products and `has_more`. The real seeded storefront passed card
rendering, Next, search with Enter, page reset, and Clear checks. When updating an
existing checkout, restart Go and apply migrations/seed as documented above;
frontend hot reload does not update the running Go process.


## First cart milestone: persistence and Go operations (implemented)

`internal/cart` provides `NewService(pool, catalogRepository)` using the existing
shared pgx pool. `Get(ctx)`, `Add(ctx, productID, quantity)`,
`SetQuantity(ctx, productID, quantity)`, and `Remove(ctx, productID)` obtain their
owner solely from `auth.FromContext(ctx)`. The context must come from the existing
access-token verifier. No operation accepts owner strings, email, or an arbitrary
cart ID. This persistence milestone added no application user records, frontend
controls, checkout operations, or automatic mutation retries. The next milestone
adds the cart HTTP adapter documented below.
Mutation methods return an error; read the cart separately to obtain its current
representation, which may include intervening concurrent changes.

New Goose migration `backend/migrations/00003_carts.sql` creates `carts` and
`cart_items`. Apply pending migrations explicitly with `go run ./cmd/migrate up`
from `backend/` using the existing trusted `DATABASE_URL`; API startup does not
migrate. Existing migrations and catalog data are unchanged. This milestone's
verification used disposable databases only; the developer database was not
migrated or reseeded.

- Each exact `(issuer, subject)` pair has at most one persistent cart, enforced by
  a unique constraint. Identity values are never normalized or replaced with email.
  `Get` returns a non-nil empty item slice, version 0, USD, and subtotal 0 if the
  cart does not exist; it does not create a database row.
- Items reference products, use integer quantities 1–99, and are unique per cart
  and product. Add requires a positive quantity and increments an existing item;
  SetQuantity requires an existing item and sets its absolute quantity. Invalid
  input or a resulting quantity outside 1–99 leaves contents/version unchanged.
  Remove is successful when the item or cart is already absent. Setting the same
  quantity is also a successful no-op. An emptied cart persists.
- The product foreign key uses **ON DELETE RESTRICT**. A referenced product cannot
  be deleted until its cart references are explicitly removed. Mark products
  unavailable instead of deleting them to preserve cart visibility. Deleting a
  cart cascades only its own items; there is no cart deletion operation yet.
- Additions and quantity increases require an available product, read through
  catalog operations. Unavailable items remain visible and may be reduced,
  removed, or left at their existing quantity. Removing an absent item requires
  no catalog lookup. The cart module never reads or writes catalog tables directly.
- Reads use the current catalog product representation and integer **int64 cents**
  for line totals and subtotal; cart_items stores no prices. The subtotal **includes
  unavailable items** and is not a checkout guarantee or an inventory reservation.
  A catalog price/availability change appears on the next read without reseeding
  and without changing the cart's content version. Orders persist immutable price snapshots during checkout. Items are returned in deterministic product-UUID order.

Every mutation uses a bounded five-second, READ COMMITTED transaction and selects
the owner's cart row **FOR UPDATE** before inspecting or changing items. Add first
inserts the cart with `ON CONFLICT (owner_issuer, owner_subject) DO NOTHING`, so
concurrent first additions converge on one row and then serialize. The row lock
prevents lost increments and quantity-limit races, and provides the lock to reuse
for checkout. Each actual content change advances the bigint cart version
once, in the same transaction as the item write. No-ops, domain rejections, and
rolled-back writes do not advance it. A failed first addition rolls back the
provisional cart too. Mutations never retry themselves: Add is not idempotent.
As with any database commit, a lost connection during commit can leave the caller
uncertain of the outcome; blindly repeating an increment is unsafe.

Lock order is cart row, then product row. For additions/increases, catalog's
`GetForShare(ctx, tx, productID)` holds a **FOR SHARE** lock through commit,
blocking concurrent price/availability updates and product deletion after the
check. If an update already holds the product lock, the mutation waits and checks
the updated availability after it commits. The check reserves no inventory;
availability can change immediately after cart commit. Reductions/removals need
no product lock. Future code that needs both resources must preserve this lock
order; checkout locks multiple products in ascending UUID order.

`Get` uses a bounded five-second read-only REPEATABLE READ transaction and
catalog's `GetInTx` operation so cart version, quantities, and product prices come
from one snapshot. It does not lock the cart or block mutations. Caller deadlines
and cancellation take precedence; rollback cleanup has its own one-second bound.
The catalog transaction methods use the supplied transaction connection, avoiding
a second pool acquisition while the cart holds locks. No generic transaction
framework was added.

Domain sentinels are `cart.ErrUnauthenticated`, `ErrInvalidProductID`, `ErrQuantity`,
`ErrUnavailable`, `ErrItemNotFound`, and `ErrTotalOverflow`. Missing products retain
`catalog.ErrNotFound`; wrapped errors support `errors.Is` for HTTP mapping.
Operational database errors remain errors rather than being reported as domain
not-found failures. The HTTP adapter maps these errors as documented below.

Verification from `backend/`:

```sh
gofmt -l cmd internal migrations
go vet ./...
go test ./...
go test -race ./...
go build ./...
# Supply a NEW empty disposable PostgreSQL 17 database, never DATABASE_URL.
CARTSIDE_TEST_DATABASE_URL='postgres://test_user:disposable_password@127.0.0.1:TEST_PORT/cart_test?sslmode=disable' \
  go test -tags=integration -race ./internal/cart -run TestPostgresCart -count=1 -v
```

The explicit integration test refuses databases containing user tables and uses
real migrations, fixture products, and principals populated by the real JWT
verifier using local signing keys/JWKS. It verifies issuer/subject isolation,
persistence across pool recreation, current prices without reseeding, unavailable
visibility/reductions/removal, integer totals, no-ops, version changes, concurrent
first creation, concurrent increments, concurrent quantity-limit enforcement,
rollback after item/version failures, database constraints, restricted product
deletion, cancellation while waiting for a cart lock, and catalog availability
lock contention. Migration rollback/reapply preserves the catalog and resets only
cart data. Use different fresh disposable databases for the catalog and catalogseed
integration suites documented above; all suites leave test data behind.

Local checks passed formatting, vet, default unit/race tests, build, and all three
explicit PostgreSQL 17.10 integration suites (cart, catalog, and catalogseed).
These historical persistence checks preceded the cart HTTP milestone. No developer records, volumes, identity-provider configuration,
or running application processes were changed. Nothing was committed or pushed.

### Authenticated cart HTTP API

APISIX exposes the following routes, stripping `/api` before forwarding to Go.
All require a gateway session and independently verified access token in Go.
Ownership comes exclusively from the verified request context.

| Method | Path | Request | Success |
| --- | --- | --- | --- |
| GET | `/api/cart` | None | 200, current cart |
| POST | `/api/cart/items` | `{"product_id":"UUID","quantity":1}` | 204, increment |
| PUT | `/api/cart/items/{product_id}` | `{"quantity":2}` | 204, set existing item |
| DELETE | `/api/cart/items/{product_id}` | None | 204, including repeated removal |

GET returns `{"version":0,"items":[],"subtotal_cents":0,"currency":"USD"}`
for an empty cart. Each item contains `product` (the catalog representation),
`quantity`, and `line_total_cents`. Prices and totals use integer cents and current
catalog prices. All cart responses use `Cache-Control: no-store`.

Authenticated unsafe application requests (all methods except GET, HEAD, and
OPTIONS) require exactly one `Origin` matching the required Go setting
`BROWSER_ORIGIN=http://localhost:9080` by scheme, case-insensitive hostname,
and effective port (HTTP 80, HTTPS 443 when omitted), plus exactly one
`X-CSRF-Protection: 1` header. Startup rejects missing or invalid configuration:
only HTTP/HTTPS with a host and optional valid port is accepted; no credentials,
path beyond an optional trailing slash, query, or fragment. Existing checkouts
must add `BROWSER_ORIGIN` to their trusted local environment and restart Go.
The Origin request header itself must have origin syntax without a trailing slash.

Missing, null, malformed, repeated, or mismatched origins return generic JSON
403 before any mutation operation. If `Sec-Fetch-Site` is present, its only
accepted value is exactly one `same-origin`; same-site, cross-site, none,
malformed, and repeated values are rejected. Without Fetch Metadata, both Origin
and the custom header remain mandatory. Host and forwarded headers never define
the trusted origin. Authentication runs separately before reusable CSRF middleware;
health and read-only requests need no CSRF headers. Gateway-owned OIDC callbacks
and logout do not reach this middleware.

Browser fetches use relative URLs, for example:

```js
await fetch('/api/cart/items', {
  method: 'POST',
  headers: {'Content-Type': 'application/json', 'X-CSRF-Protection': '1'},
  body: JSON.stringify({product_id: selectedProduct.id, quantity: 1})
});
```

The browser supplies Origin and Fetch Metadata itself. This custom-header policy
is not a secret CSRF token: the fixed value `1` forces cross-origin browser code
to preflight, while Origin and Fetch Metadata checks enforce the configured
browser boundary. Neither Go nor APISIX enables CORS; do not enable cross-origin
credentialed CORS for these routes. This does not protect against same-origin XSS
or authenticate non-browser clients.

Curl mutations also need Origin and the custom header, plus an authenticated
APISIX session cookie jar (kept private); curl does not supply browser headers
automatically. For example, setting an existing item's quantity:

```sh
curl -i --cookie "$CARTSIDE_COOKIE_JAR" -X PUT http://localhost:9080/api/cart/items/PRODUCT_UUID -H 'Origin: http://localhost:9080' -H 'X-CSRF-Protection: 1' -H 'Content-Type: application/json' --data '{"quantity":2}'
```

If manually sending `Sec-Fetch-Site`, use `same-origin`. Direct authenticated
Go requests have the same CSRF requirements. APISIX preserves Origin,
X-CSRF-Protection, and Sec-Fetch-Site as received and never manufactures them.
Its API route removes cookies before Go and forwards the gateway-generated access
token. Existing upstream `retries: 0` remains in effect.

POST/PUT require `application/json` (media type parameters are allowed), at most
4096 body bytes, exact field names, a canonical hyphenated UUID, and integer
quantities 1–99. Missing, null, unknown, duplicate (including escaped equivalent
names), and trailing JSON fields/values are rejected. PUT accepts only quantity.
Successful mutations return an empty 204 directly from the operation result;
clients fetch GET afterward. **POST increments are not safe to retry
automatically**, including after an ambiguous network failure or failed GET.

Errors use `{"error":"message"}`: 400 invalid input/quantity, 401 missing or
invalid authentication, 403 failed CSRF checks, 404 missing product or existing
cart item, 409 unavailable product for an increase, 413 oversized body, 415 wrong
media type, and 503 operational failure. Internal error details are not exposed.
The cart UI is implemented below; simulated checkout supports explicit review, confirmation, and recovery of uncertain attempts.


## Storefront cart milestone (implemented)

Available product cards add one unit. The accessible Your cart section shows each
product's package, current unit price, quantity, line total, availability, and
subtotal. Total units and distinct item counts are separate. Quantities are whole
numbers from 1–99, saved with an explicit Update action; Remove is separate.
Unavailable items stay visible and can be reduced or removed, with increases
disabled. Subtotal includes unavailable items; prices are confirmed again at
checkout. No checkout control is included.

The storefront shares one cart state without an additional state library. It
loads GET `/api/cart` and reads again after every successful 204 mutation. Requests
use relative URLs, same-origin credentials, no-store caching, and eight-second
timeouts. POST/PUT send JSON; all mutations send `X-CSRF-Protection: 1`. The browser
supplies Origin and Fetch Metadata. Product/cart shapes and safe integer-cent
totals are validated before display with `Intl.NumberFormat`.

Mutations are serialized with both a synchronous guard and disabled controls,
including during the follow-up read. No optimistic updates or automatic mutation
retries occur. Obsolete reads cannot replace newer state. Search, pagination,
identity, and logout remain independent. Changes update this browser immediately;
there is no polling or synchronization between tabs or devices. Refresh cart is
a read-only action to load external changes.

Loading, empty, read failure, mutation rejection, and expired sessions have
separate messages announced without moving focus. Quantity inputs retain their
DOM identity. A saved mutation whose follow-up read fails reports “Change saved,
cart refresh failed” and disables edits until Refresh cart succeeds. Network,
timeout, unexpected success responses, and server failures are treated as
uncertain mutation outcomes: the server may have committed. The UI explains this,
automatically attempts a read (never a repeat mutation), and requires successful
reconciliation before further changes. Aborting a timed-out request does not
cancel its server-side effects.

Use Node 24 and the existing infrastructure/setup instructions above. Apply the
existing cart migration and restart Go from this checkout before testing; Vite
hot reload does not replace a running Go binary. From `frontend/`:

```sh
npm ci
npm run dev
# In another terminal, also from frontend/:
npm run lint
npm run typecheck
npm test
npm run build
```

Vitest with jsdom and React Testing Library covers additions, absolute updates,
removal, cents/subtotals, unavailable items, quantity validation, stable focus,
search preservation, rapid duplicate clicks, obsolete reads, request JSON/headers,
saved changes with failed refresh, uncertain outcomes and reconciliation, session
expiry, CSRF rejection, other HTTP errors, and bounded timeouts. CI runs the test
command in its Frontend job.

Verification for this milestone: Node 24 lint, typecheck, all 17 frontend tests,
and production build passed. Headless Chrome through APISIX with a newly
registered isolated account and controlled cart responses verified keyboard
addition/update, removal, reload rendering, search preservation, and no horizontal
overflow at 375px/1280px. Registration, catalog, identity, logout, and signed-out
cart protection used the real services. The authenticated running backend returned
404 for `/api/cart`; live add/update/remove persistence and two-user ownership
checks could not be verified. Existing processes, developer records, and volumes
were preserved; no migrations or seeds were run. Only new verification accounts
were registered. Those live cart checks were subsequently completed as recorded below. Screen-reader announcement
behavior has automated markup coverage, but was not tested with a screen reader.


### Live cart verification follow-up (2026-10-15)

The authenticated 404 came from `/tmp/cartside-search-api-final`, an older Go
binary without cart routes. The local database also lacked migration 3, and the
local `.env` lacked `BROWSER_ORIGIN` and the OIDC settings previously supplied
separately to the process. Added the documented non-secret origin/OIDC settings
without replacing secrets, applied the single pending migration 3, and gracefully
replaced the old backend with a binary built from the current checkout. Gateway
readiness and authenticated GET `/api/cart` now return 200. Existing products,
developer records, and persistent volumes were preserved; no seed or reset ran.

Headless Chrome through real APISIX/Keycloak/Go/PostgreSQL, without mocked cart
responses, passed keyboard addition and absolute quantity update, removal, cart
persistence across page reload, submitted-search preservation during mutations,
375px/1280px layouts without horizontal overflow, and logout followed by cart 401.
Two additional newly registered isolated accounts verified independent carts:
account B could not update account A's item (404), removing B's absent item did
not affect A, and both accounts held different quantities of the same product.
A browser mutation without the custom CSRF header returned 403 and left the cart
unchanged. Verification cart items were removed and the accounts signed out;
new test identities and their empty carts remain. No developer cart was used.
The live persistence/ownership verification gap is resolved. Screen-reader testing
remains unperformed; the automated failure-path coverage above still applies.


### Simulated checkout operations

`internal/orders.Service.Checkout` is exposed through the authenticated checkout
HTTP adapter documented below. Migration `00004_orders.sql` adds simulated
orders and immutable item snapshots. The checkout UI is documented below. No payment, tax, shipping,
delivery, inventory reservation, AI, or idempotency expiry/cleanup is implemented.
Apply migrations with the existing `go run ./cmd/migrate up` command.

The input contains a case-sensitive idempotency key (1–128 printable ASCII
characters without spaces), expected nonnegative cart version, and nonempty
reviewed product IDs, quantities (1–99), and nonnegative unit prices in cents.
UUIDs are canonicalized, items sorted by product UUID, and duplicates rejected.
A SHA-256 fingerprint covers a versioned canonical JSON representation of the
expected cart version and all reviewed IDs, quantities, and prices. Item order
and UUID casing do not change the fingerprint. Reviewed values are preconditions;
prices, names, package labels, currency, and totals come from locked catalog data.
Money uses checked int64 cents. Current catalog int32 prices and bounded
quantities permit wide line totals; multiplication and sum overflow are checked
before arithmetic, and database constraints enforce valid cents and line totals.

Ownership is derived only from `auth.FromContext`. Each attempt uses one bounded
five-second READ COMMITTED pgx transaction, shared with narrow cart operations
and catalog's existing `GetForShare`. Checkout first looks up an order by verified
issuer, subject, and key, then locks the owner's existing cart FOR UPDATE and
rechecks the key. It rejects missing/empty carts, stale versions, mismatched
membership/quantities, unavailable products, and changed prices. Product rows
are locked FOR SHARE in ascending UUID order until commit. Order/item inserts,
cart clearing, and exactly one cart-version increment commit atomically. Failures before commit roll back the whole transaction; a commit error may leave
the outcome unknown. Independent one-second rollback cleanup
releases locks even after cancellation. Cart mutations retain their existing API
and use the same cart-before-product lock order.

The database uniquely constrains issuer + subject + idempotency key. Concurrent
same-key requests converge on one confirmation; different keys for one reviewed
cart can produce at most one order. Successful same-key/same-fingerprint replay
returns the original snapshots before checking the current cart, including after
cart changes or catalog edits/deletion. Different input returns
`orders.ErrIdempotencyConflict`. Orders have UUID IDs, `SIM-` confirmation IDs,
creation timestamps, a `simulated` kind, and USD totals. Item product UUIDs have
no catalog foreign key, so history survives deletion. Database triggers reject
updates and deletes of order records and snapshots.

Domain errors are exported sentinels usable with `errors.Is`: unauthenticated,
invalid input, idempotency conflict, empty cart, stale version, reviewed-item
mismatch, unavailable product, changed price, and overflow. Storage failures
return generic `ErrStorage`; caller cancellation/deadlines remain context errors.
A commit error can be ambiguous: checkout never automatically retries a commit
or creates a new key. Callers retry with the original key and input to resolve
the outcome safely.

From `backend/`, run the default checks above plus this explicit suite against
its own **new empty disposable PostgreSQL 17 database**, never `DATABASE_URL`:

```sh
CARTSIDE_TEST_DATABASE_URL='postgres://test_user:disposable_password@127.0.0.1:TEST_PORT/orders_test?sslmode=disable' \
  go test -tags=integration -race ./internal/orders -run TestPostgresCheckout -count=1 -v
```

The integration suite refuses nonempty databases and leaves test data behind.
It verifies confirmations, snapshots, cart clearing/versioning, replay after cart
and catalog changes/deletion, conflicting reuse, identity-pair isolation,
precondition failures, concurrent same/different-key checkout, wide integer cents,
and version-overflow rollback. A controlled cart-version constraint failure after
order insertion and cart deletion proves both writes roll back together. Unit
tests cover canonical fingerprints and monetary multiplication/addition overflow;
actual monetary int64 overflow would require impractical cart sizes under the
current catalog price and quantity constraints. Cart and catalogseed migration
rollback tests include the new migration; each integration package needs its own
fresh database.

Checkout milestone verification passed: formatting, `git diff --check`,
`go vet ./...`, default unit tests, `go test -race ./...`, `go build ./...`,
and all four explicit integration packages (orders, cart, catalog, catalogseed)
with the race detector against separate fresh databases in a disposable
PostgreSQL 17.10 container on an ephemeral loopback port. Developer databases,
volumes, and application services were untouched. No HTTP/frontend changes,
commit, push, or deployment were performed.


### Simulated checkout API (React review-screen contract)

`POST /api/checkout` through APISIX reaches `POST /checkout` in Go. Use the
existing browser session, `Content-Type: application/json`,
`X-CSRF-Protection: 1`, and exactly one `Idempotency-Key` header. The browser
supplies its same-origin `Origin`; if `Sec-Fetch-Site` is present it must be
`same-origin`. Go requires the gateway-forwarded, independently verified token;
never supply owner identity in the body. APISIX forwards the idempotency header
unchanged and retains `retries: 0`.

The client creates and retains an opaque, case-sensitive key for each reviewed
checkout attempt: 1–128 printable ASCII characters, excluding whitespace.
The server neither creates keys nor retries checkout automatically. Submit the
version and complete item list from the customer's review, with integer values:

```json
{
  "expected_cart_version": 3,
  "items": [
    {"product_id": "bb5ce88f-908c-5a99-8ae7-90b60b00ae1b", "quantity": 2, "unit_price_cents": 399}
  ]
}
```

Version and cents are nonnegative signed 64-bit integers; quantities are 1–99.
The list must be nonempty and product UUIDs unique after canonicalization.
All fields are required and non-null. Unknown/duplicate fields at either object
level, fractions/exponents, strings in numeric fields, overflow, malformed UUIDs,
and trailing JSON are rejected. Integers are decoded directly without floating
point. The body limit is **16 MiB**, including whitespace; there is no item-count
cap. This is a transport byte limit, not a cart business rule; requests exceeding
it must be reduced in encoding size or the transport limit raised, never split
into partial checkouts. The orders service remains authoritative for validation.

Both initial completion and successful replay return **200**,
`Content-Type: application/json`, and `Cache-Control: no-store`:

```json
{
  "id": "d550df71-4280-480b-a733-1ee7e24c1ab5",
  "confirmation": "SIM-example",
  "kind": "simulated",
  "created_at": "2026-10-15T12:00:00Z",
  "currency": "USD",
  "total_cents": 798,
  "items": [
    {
      "product_id": "bb5ce88f-908c-5a99-8ae7-90b60b00ae1b",
      "name": "Apples",
      "package_label": "bag",
      "quantity": 2,
      "unit_price_cents": 399,
      "line_total_cents": 798
    }
  ]
}
```

`created_at` uses RFC3339. Items are immutable catalog snapshots in product-UUID
order. The response excludes owner identity, keys, fingerprints, and tokens.
Checkout returns the order directly; it does not require a follow-up cart read.
After success the UI can display this confirmation and clear its local cart;
a later cart refresh is independent of checkout success.

Checkout handler errors use `{"code":"prices_changed","message":"Prices changed; review the cart again."}`
with safe messages and `Cache-Control: no-store`. Codes are stable; do not branch
on message text.

| HTTP | Code | Meaning/action |
| --- | --- | --- |
| 400 | `invalid_input` | Invalid headers/body, body too large, or numeric overflow; correct the request. |
| 401 | `unauthorized` | Verified authentication required. |
| 409 | `empty_cart` | No items to check out. |
| 409 | `stale_cart_version` | Cart changed; review again. |
| 409 | `reviewed_items_mismatch` | Reviewed membership/quantities differ; review again. |
| 409 | `products_unavailable` | A reviewed product is missing or unavailable. |
| 409 | `prices_changed` | A reviewed price changed; review again. |
| 409 | `idempotency_conflict` | Successful key reused with different canonical input. |
| 503 | `checkout_unavailable` | Storage/dependency failure or cancellation; outcome may be unknown. |
| 504 | `checkout_timeout` | Deadline exceeded; outcome may be unknown. |

Shared CSRF middleware retains its existing **403** JSON
`{"error":"CSRF protection required"}`. APISIX session rejections and gateway
errors retain their existing responses; clients must also handle non-contract
responses, connection loss, and gateway timeouts.

**Safe retries:** retain the exact same key and original body after a timeout,
connection loss, 503/504, or any uncertain outcome. Do not assume an error means
rollback, clear the cart optimistically on error, or generate a replacement key
to resolve an uncertain result. A successful retry returns the original order,
even if the cart was cleared or changed meanwhile. Retry is user-driven, not
automatic. After a definitive review conflict, refresh/review the cart and obtain
explicit customer acceptance before starting a new attempt with a new key.
Never send the old successful key with newly reviewed input.

Handler integration tests use their own empty disposable database:

```sh
CARTSIDE_TEST_DATABASE_URL='postgres://test_user:disposable_password@127.0.0.1:TEST_PORT/checkout_http_test?sslmode=disable' \
  go test -tags=integration -race ./internal/httpapi -run TestPostgresCheckoutHTTP -count=1 -v
```


Checkout API verification (2026-10-15): formatting, vet, unit tests, race tests,
and build passed. All five explicit integration suites (orders, cart, catalog,
catalogseed, checkout HTTP) passed with the race detector, each using a separate
fresh database in a disposable PostgreSQL 17.10 container. JSON/YAML parsing,
Compose validation, and APISIX configuration tests passed. A headless Chrome
session through a separate APISIX/Keycloak stack verified real sign-in, 401 and
403 enforcement, checkout 200 and identical replay, changed-body/key conflict,
price-failure cart preservation, exactly one cart clearing/version increment,
and public health checks. An opaque mixed-case key containing a comma passed
through unchanged. Test resources were removed; developer services, users,
configuration, databases, and volumes were preserved. No frontend changes,
commit, push, or deployment were performed.


### Simulated checkout frontend

A successfully loaded, nonempty cart offers **Review simulated order**. Review
fetches the current cart and copies its product names, packages, quantities,
unit prices, line totals, subtotal, and cart version. Unavailable products block
confirmation. Local cart changes invalidate that review. Back to cart returns
focus to the cart; **Place simulated order** is the explicit acceptance action.
No payment, tax, delivery, or real purchase occurs.

Before sending, the browser generates `crypto.randomUUID()`, serializes the
reviewed version and complete IDs/quantities/prices once, and writes only that
key and exact body to sessionStorage. Checkout uses relative `/api/checkout`,
same-origin credentials, the existing CSRF header, and an eight-second timeout.
A synchronous shared lock prevents cart mutations during review loading,
submission, and unresolved outcomes. Checkout never retries automatically.

A lost connection, timeout, server/gateway error, or invalid confirmation leaves
the attempt unresolved. **Retry same checkout** reuses its key and exact body;
cart reads and even an empty cart never establish that checkout failed. There
is no start-over action. A 401 prompts sign-in while retaining the attempt for
recovery after session renewal. Unmarked 400/403 responses preserve recovery for
investigation; marked cart/price/availability conflicts refresh the cart and
require a new explicit review. An idempotency conflict retains the
original attempt as an exceptional condition requiring investigation. A retry
with a marked Go cart rejection after same-key order lookup resolves the
earlier uncertainty, refreshes the cart, and requires renewed review. Unmarked
gateway/network responses retain the original attempt. See [the E2E guide](docs/e2e.md#recovery-contract).

Recovery waits for verified `/api/me` issuer + subject and reads only that
identity's namespaced record. Another signed-in user cannot replay the record
through the UI. Invalid records or unavailable restoration storage block checkout
and mutations with an explanation. If writing/verifying recovery storage fails,
no checkout request is sent. No tokens or identity assertions are stored.

sessionStorage recovery is limited to the same browser tab/session and origin.
Closing the tab, clearing site data, or losing browser storage can lose the
recovery record; it is not a durable recovery service. A newly opened tab may
inherit a copy from its opener. Independent tabs/devices do not coordinate UI
locks; backend version checks and idempotency remain authoritative. Keep an
unresolved tab and its storage until the same-key retry resolves the outcome.

A validated immutable response displays its simulated confirmation, timestamp,
item snapshots, and total. Only then is pending recovery data removed. Cart
refresh is independent: failure retains the confirmation and disables mutations
until a successful read-only Refresh cart. Continue shopping returns focus to
search. Confirmation exists only in component state, so reloading after success
does not provide order history. If recovery-record removal fails after success,
the UI explains that a later reload may safely replay the confirmed attempt.

Frontend verification (2026-10-15): lint, type-check, 37 tests, and production
build passed using Node 24. Tests cover review input, explicit acceptance,
duplicate clicks, headers, timeouts, exact retries, reload/identity isolation,
session renewal, storage failures, changed prices, cart locking, invalid/gateway
responses, and confirmed checkout with failed cart refresh. Headless Chrome
through the existing APISIX/Keycloak stack verified real registration/login,
cart operations, keyboard review/confirmation and focus, 375/1280px layout,
reload recovery with exact key/body reuse, Continue shopping, and logout.
Checkout responses in that browser recovery check were controlled: the running
host backend returned 404 for checkout, so live checkout persistence/replay could
not be verified in this milestone. Existing services/configuration/data were
preserved; new verification identities remain (one initial probe cart remains;
the controlled run's cart was emptied). Screen-reader and live session-expiry
browser testing remain unperformed; session renewal is covered in frontend tests.
The live frontend-to-checkout gap was closed by the isolated stabilization suite
on 2026-10-18. Order browsing/history, AI suggestions/chat, live inventory,
and cross-tab/device coordination are planned or out-of-scope capabilities, not
verified shopping defects. Payments and delivery remain outside scope.

## AI milestone 1: local mock protocol path

The first AI milestone uses the pinned APISIX `ai-proxy` plugin with an
OpenAI-compatible Chat Completions contract and a deterministic Go provider in
Compose. `/internal/ai/chat/completions` requires `CARTSIDE_AI_SERVICE_KEY`;
storefront sessions do not authorize it. There are no paid services, real API
keys, browser assistant features, catalog IDs, or cart changes. See the
[AI guide](docs/ai-mock.md) for architecture, scenarios, exact setup/verification
commands, and pinned-plugin compatibility limits.


## Authenticated assistant endpoint

Enable `CARTSIDE_ASSISTANT_ENABLED=true` in the host Go environment and configure
`CARTSIDE_AI_GATEWAY_URL` plus `CARTSIDE_AI_SERVICE_KEY` as in `.env.example`.
Default `false` constructs no AI client and requires no AI credentials in Go;
authenticated, CSRF-valid calls return 503 `assistant_unavailable`. Compose's
internal AI route still requires its independent service key. Enabled startup
validates gateway configuration and positive limits without invoking inference.
Normal catalog/cart/checkout operations and health do not depend on inference.

The embedded chat panel calls `POST /api/assistant/messages` on the application
origin with browser credentials, `Content-Type: application/json`,
`X-CSRF-Protection: 1`, and exactly `{"message":"breakfast for two"}`. Go receives
`/assistant/messages`. The decoded message must be nonblank and at most **256
UTF-8 bytes**, not characters; the JSON body is capped at 2048 bytes. For example,
128 `é` characters fit; 129 do not. Escaped and literal Unicode use the same
decoded byte count; invalid UTF-8 and unpaired surrogate escapes are rejected.
Unknown/duplicate fields, missing/null values, trailing JSON, roles, history,
model, provider URLs, candidates, and owner identity are rejected.

Success is the existing version 1 result: `version`,
`provider: "simulated: cartside-mock-v1"`, `text`, and `suggestions` (always an
array). Suggestions contain `product_id`, `quantity`, `reason`, `name`,
`package_label`, `price_cents`, `currency`, and `available`, refreshed from the
catalog. All responses use `Cache-Control: no-store`. This endpoint never changes
carts or orders. React renders returned text as text. Individual acceptance uses
the existing cart API; no acceptance endpoint, persistence, streaming, or
automatic retries are added.

The single Go process defaults to 10 admitted requests per minute per verified
issuer + subject, one in-flight request per user, and 1000 identity entries.
Configure positive `CARTSIDE_ASSISTANT_REQUESTS_PER_MINUTE` and
`CARTSIDE_ASSISTANT_MAX_USERS` values. Authentication, CSRF, and input validation
precede admission; admitted failures still count. Idle entries are lazily evicted on admission after
their fixed minute window; busy entries remain protected. Capacity exhaustion
fails closed for new users. Restart resets allowances. Multiple Go instances
require shared enforcement. This is a Go policy, not an APISIX per-user quota;
gateway quotas can be added separately. Service authentication remains intact.

Errors use `{"code":"...","message":"safe explanation"}`:

| HTTP | Code | Meaning |
| --- | --- | --- |
| 400 | `invalid_input` | Invalid content type, JSON, or message |
| 401 | `unauthorized` | Verified authentication required |
| 403 | `csrf_required` | Existing origin/header CSRF checks failed |
| 413 | `payload_too_large` | JSON body exceeds 2048 bytes |
| 429 | `assistant_rate_limited`, `assistant_busy`, `assistant_capacity` | Local admission rejected |
| 429 | `provider_rate_limited` | Provider returned 429 |
| 503 | `assistant_unavailable` | Feature disabled |
| 504 | `timeout` | Provider request timed out |
| 502 | `provider_unavailable`, `invalid_provider_output` | Provider failure or invalid/oversized response |
| 503 | `catalog_unavailable` | Catalog access failed |
| 409 | `catalog_changed` | Suggested product disappeared or became unavailable |

429 responses include `Retry-After` seconds. Busy/capacity/provider errors use a
one-second hint (not a guarantee); the pinned gateway discards upstream retry
headers. Local rate errors report the remaining fixed window. The panel should
keep manual shopping usable on failure and offer deliberate retries, respecting
`Retry-After`. Gateway authentication failures can retain gateway-native bodies;
handle status even when the application error envelope is absent. Cancellation
propagates to catalog/provider work and releases the in-flight slot.

Demo messages: `breakfast for two`, `vegetarian dinner`, `snacks for a picnic`.
Other valid messages return local clarification with `suggestions: []`. The mock
is deterministic and does not verify dietary/allergen suitability, natural
language quality, billing, or live-provider compatibility. See [AI details and
verification](docs/ai-mock.md).


## Embedded demo assistant and suggestion acceptance

The storefront now includes **Demo shopping assistant** after the catalog and
before the cart. Start local infrastructure as documented above, export your
trusted `.env` into the Go shell, and set `CARTSIDE_ASSISTANT_ENABLED=true`
before restarting Go. Keep `CARTSIDE_AI_GATEWAY_URL` and the matching
`CARTSIDE_AI_SERVICE_KEY` in the host environment only. Start Vite and sign in
through APISIX. No frontend credentials or feature flags are needed. With the
backend flag unset or `false`, the panel shows a safe disabled response when
asked; normal browsing, cart controls, and checkout continue to work.

Select an example to fill the labeled input, then explicitly **Send message**:
`breakfast for two`, `vegetarian dinner`, or `snacks for a picnic`. Responses
are deterministic simulations, not a live language model. Dietary and allergen
suitability remains unverified; inspect product labels. Unsupported prompts
produce clarification rather than natural-language meal planning.

The input counts UTF-8 bytes (256 maximum), including multibyte characters, and
rejects blank text and unpaired surrogates. Only the current message is sent.
There is no conversation context on the server. The panel validates version 1,
the exact simulated-provider label, text, suggestion fields, unique product IDs,
USD integer-cent prices, availability, and whole quantities from 1–99 before
rendering. User/model content is always text, never HTML. It displays each
suggestion's catalog name, package, unit price, quantity, and reason.

Receiving suggestions makes no cart request. **Decline** changes only local UI
state. **Accept** adds exactly that suggestion's quantity through the shared
cart mutation path; Go still validates products, availability, quantities, and
prices. Each suggestion has independent actions; there is no Accept All.
Pending or confirmed additions cannot be accepted twice. Cart loading,
mutations, failed reconciliation, checkout submission, and unresolved checkout
block acceptance. An accepted change invalidates any existing checkout review
through the same shared cart state as manual changes.

The cart hook now returns a small `MutationResult` contract:

| Outcome | Panel behavior |
| --- | --- |
| `not-started` | No mutation sent; leave the suggestion retryable when controls are ready |
| `confirmed` | Mark accepted; `refreshed` separately reports whether the follow-up cart GET succeeded |
| `rejected` | Show the relevant cart error; allow a deliberate retry subject to current locks |
| `uncertain` | Keep **Outcome uncertain** and disable Accept/Decline for that suggestion; inspect and manually adjust the cart |

A confirmed addition remains accepted even if cart refresh fails. The cart
message reports the actual added quantity. A successful GET after a lost,
invalid, or failed mutation response cannot prove that a particular increment
committed, so it never makes an uncertain suggestion retryable. Mutations are
never automatically retried. Message failures can be deliberately resent;
resending a prompt is distinct from retrying an uncertain addition. Do not use
new suggestions or a reload as a substitute for inspecting uncertain cart changes.

The transcript is component memory only, limited to 12 exchanges (at most 24
user/assistant messages and 60 suggestions). Pending and uncertain additions are
protected from eviction; older resolved exchanges are removed first. If all 12
are protected, new messages stop rather than losing outcome information.
Reloads, unmounts, and verified account changes discard the transcript and local
accept/decline markers. Late assistant/acceptance responses cannot populate the
next identity's panel. There is no cross-tab coordination or durable acceptance
receipt. The UI disables concurrent sends, aborts message work after eight
seconds or unmount, handles safe application/gateway failures, and shows a
`Retry-After` cooldown without automatically resending anything.

Verification commands:

```sh
cd frontend
npm run lint
npm run typecheck
npm test
npm run build
cd ..
# Uses disposable users, databases, ports and Compose resources; never developer .env.
/path/to/python-with-pyyaml scripts/e2e.py
```

Use the Python environment setup in [the AI guide](docs/ai-mock.md) for the last
command. The explicit browser suite sends a prompt through the public APISIX
route, Go, the service-authenticated APISIX AI route, and the mock, accepts one
suggestion, declines another, and checks the shared cart. It also retains the
session-only internal-route denial, cart/order snapshots before acceptance,
manual shopping, identity isolation, and checkout recovery regression scenarios.

Order browsing and broader assistance beyond the three deterministic scenarios
are future capabilities, outside the original mock-assisted MVP exit criteria.
Broader usability/accessibility review remains unverified. Streaming,
persistent conversations, real providers, and AI-driven checkout are outside
this milestone. The current assistant is a deterministic demo, not a live-model
integration or a dietary recommendation system.


Verification for the embedded assistant milestone (2026-10-18): Node 24.15.0
lint, application/E2E type checks, 83 unit tests, and production build passed.
The disposable Go 1.24.0 full-stack run passed all six database integration
packages with race detection and all five Chromium shopping tests, including
prompt → individual acceptance/decline → shared cart. Configuration parsing and
whitespace checks passed. Developer data was preserved. Screen readers, other
browser engines, live providers, and remote CI were not exercised. See the
[AI verification record](docs/ai-mock.md#embedded-panel-milestone).

## Optional local monitoring

The `observability` Compose profile adds Prometheus 3.13.1 and Grafana 13.2.3,
with committed data source/dashboard provisioning and persistent named volumes.
Normal shopping needs neither service nor Grafana credentials. Follow
[the observability guide](docs/observability.md#local-metrics-and-dashboard) to
create the untracked password file, run host Go with `METRICS_ADDR=0.0.0.0:9091`,
and start `docker compose --profile observability up -d prometheus grafana`.
Open http://localhost:3000/d/cartside-overview as `local-admin` using that private
password. Grafana binds localhost; Prometheus and APISIX metrics have no host port.
The host Go scrape listener needs inbound firewall restrictions. Prometheus uses
15s scrapes, 5s timeouts and 7d/1GB retention. API/gateway traffic overlaps and must
not be summed. Health probes are separate; synthetic mock tokens/costs are omitted.
Use dashboard latency/outcomes to locate a time window, then the browser response's
`X-Request-ID` to join API, gateway and mock completion logs. The guide documents
metrics, buckets, no-data behavior, credentials and exact verification limitations.

## Single-VM demo packaging

A separate ARM64 Compose package, API/static frontend Dockerfiles, private services,
HTTPS edge configuration and disposable HTTPS verification runner are available.
See [the deployment guide](docs/deployment.md) for fresh-database setup, identity
provisioning, secrets, verification evidence and launch blockers. Packaging does
not provision Oracle resources, change DNS, request public certificates or publish
the demo. Native ARM64 images, the six-scenario HTTPS suite before/after restart,
restricted runtime/migration database roles, private monitoring and orderly shutdown
were verified on 2026-10-24. See [executed checks, digests and resource measurements](docs/deployment-verification.md).
Backup/restore, public registration protections, secret/administrator recovery,
target-host sizing, certificate renewal and public-launch authorization remain gates.
