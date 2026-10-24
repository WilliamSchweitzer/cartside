# Request correlation and structured logs

Cartside combines bounded local Prometheus metrics, a provisioned Grafana overview,
and `X-Request-ID` correlation in safe JSON logs. There are no tracing spans,
log aggregation, external monitoring services, or alert notifications. Request IDs
never become metric labels.

## Local metrics and dashboard

Normal shopping and `docker compose up -d --wait` need no monitoring configuration
or Grafana password. The optional `observability` profile runs Prometheus **3.13.1**
(LTS branch) and Grafana **13.2.3**. APISIX remains pinned to **3.14.1**.
See the [Prometheus support policy](https://prometheus.io/docs/introduction/release-cycle/)
and [Grafana release](https://github.com/grafana/grafana/releases/tag/v13.2.3).

Create the initial Grafana administrator password outside Git:

```sh
mkdir -p .local
python3 - <<'PYCODE'
from pathlib import Path
import secrets
p = Path('.local/grafana_admin_password')
if not p.exists():
    p.write_text(secrets.token_urlsafe(32) + '\n')
    p.chmod(0o644)
Path('.local').chmod(0o700)
PYCODE
# With the normal Go environment loaded, restart the host API:
cd backend
METRICS_ADDR=0.0.0.0:9091 go run ./cmd/api
# In another terminal, from the repository root:
docker compose --profile observability up -d prometheus grafana
```

Start ordinary infrastructure as described in README first. Open
http://localhost:3000/d/cartside-overview and sign in as `local-admin` (or
`GRAFANA_ADMIN_USER`) with the password in `.local/grafana_admin_password`.
The host directory is private (0700); the file is readable by Grafana's container UID (0644). `GRAFANA_PASSWORD_FILE` overrides that path. Do not use example credentials.
Grafana creates the administrator only on first initialization of its named volume;
changing the file later does not reset an existing administrator password. Use
Grafana's password change flow for an initialized instance. Anonymous access and
self-registration are disabled. The profile's password file is not needed by
ordinary Compose startup. Stop only monitoring with
`docker compose --profile observability stop prometheus grafana`; data persists.
Do not use `down -v` on your developer project.

`METRICS_ADDR` defaults to empty (disabled). It creates a separate HTTP listener
serving `GET /metrics`; it is never added to the application router, Vite, or the
OIDC routes. Both servers shut down on process termination; a metrics bind failure
fails startup. The API's database readiness and shutdown behavior remain intact.
`0.0.0.0:9091` allows the Prometheus container to reach host Go via
`host.docker.internal:9091`; Compose provides the Linux host-gateway mapping.
This also exposes unauthenticated metrics to networks allowed by the host firewall.
Restrict inbound TCP 9091 to trusted local/Docker sources. Loopback-only binding
is preferable when scraping directly on the host but is generally unreachable
from containers. This setup is local development infrastructure, not a secured
monitoring deployment.

Grafana publishes only `127.0.0.1:3000`; Prometheus has **no host port** and APISIX's
export listener `apisix:9091/apisix/prometheus/metrics` stays inside the Compose
network. Containers on that network can scrape without login. For temporary local
Prometheus debugging, add a private Compose override publishing
`127.0.0.1:9090:9090` on `prometheus`; then visit `/targets` or `/graph` at
http://localhost:9090. Remove the override afterward. Never publish APISIX's
export port or put metrics behind the frontend catch-all.

Prometheus scrapes every **15s**, with a **5s** timeout. Retention is bounded by
**7 days or 1GB**, whichever is reached first (block/WAL overhead means this is not
a strict filesystem quota). `prometheus_data` and `grafana_data` are named volumes.
Provisioning files and the dashboard JSON are committed under `infra/observability`.
Grafana uses the internal Prometheus URL with a fixed data source UID; no manual
imports or browser credentials in provisioning are needed.

### Go metric contract

The maintained `prometheus/client_golang` client (v1.23.2, compatible with this
repository's Go 1.24 baseline) uses one dedicated registry per API process,
including Go and process collectors registered once. No global registry is used.
Metrics attach to the existing completion defers, not additional nested handlers.

| Metric | Labels / meaning |
| --- | --- |
| `cartside_http_requests_total` | `method`, Chi `route`, `status`; completed incoming API requests |
| `cartside_http_request_duration_seconds` | Same labels; classic histogram of completion time |
| `cartside_http_requests_in_flight` | No labels; active incoming API requests, including probes |
| `cartside_assistant_calls_total` | Fixed `operation=shopping_completion`, `provider=mock`, bounded `outcome` |
| `cartside_assistant_call_duration_seconds` | Same labels; complete outbound client operation including response validation |

HTTP methods are GET/POST/PUT/PATCH/DELETE/OPTIONS/HEAD or OTHER. Status is
1xx–5xx, or `interrupted` when no response status exists during stack unwinding.
Cancellation records the observed handler response, consistent with completion
logs; an implicit completed response is 200 even if the peer disconnected.
Routes use templates, never raw paths. Unknown paths and authentication rejection
before routing use `unmatched`; this intentionally cannot identify the rejected
URL. No query, prompt, request ID, identity, product ID, or error string is a label.
Assistant outcomes use the bounded values documented under Events below; each
client invocation counts once, including cancellation and validation failures.
Disabled assistant requests do not create outbound calls.

API histogram finite buckets (seconds):
`0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 3, 5, 10`.
They cover fast local operations, the 1s readiness timeout, assistant work, the
5s gateway API timeout, and 10s API read/write timeouts. Assistant buckets:
`0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 1.5, 2, 3`, focused around the
1s gateway provider timeout and 2s client timeout. Both include automatic +Inf;
HTTP server timeouts do not guarantee that a handler stops at exactly that limit.

### Gateway contract and dashboard interpretation

A global Prometheus plugin rule covers all routes. The pinned
[plugin schema](https://github.com/apache/apisix/blob/3.14.1/apisix/plugins/prometheus.lua)
only supports `prefer_name`; it has no per-label disable switch. We keep IDs,
configure no extra labels, and use actual exported `apisix_http_status` counters
and `apisix_http_latency_bucket` histograms. Latency is **milliseconds**, with
`type=request` selected for end-to-end gateway timing. The
[exporter](https://github.com/apache/apisix/blob/3.14.1/apisix/plugins/prometheus/exporter.lua)
uses configured route templates for `matched_uri`, not arbitrary request paths.
Its unavoidable labels include `consumer`, `node` (upstream IP), `matched_host`,
service, request type and model metadata. Current routes have bounded static IDs,
no APISIX consumers, no user-selected model, and fixed upstreams; upstream IPs can
change on restarts. Dynamic upstreams, consumers, or model selection would require
a renewed cardinality/privacy review. No extra potentially sensitive labels are
enabled. AI calls bypass ordinary Nginx upstream timing; use request latency and
Go outbound metrics. Prometheus drops `apisix_llm_.*` series: mock usage is
synthetic and cannot measure real model tokens or cost. There are no token/cost panels.

The overview shows scrape health, API rate/5xx errors, API p50/p95, in-flight
requests, assistant rate/failure outcomes/p50/p95, and gateway rate/5xx/p95.
API and gateway panels describe overlapping boundaries; **never sum them as
independent customer requests**. Gateway includes frontend, auth and internal AI.
Health routes are excluded from traffic/latency panels; in-flight includes them.
An empty panel can mean no calls/outcomes yet, fewer than two scrapes, or a down
target. Zero means a present series with no activity. Empty quantiles/NaN indicate
no bucket observations in the rate window. Look at scrape health first, then
create traffic and allow the 15s APISIX export-cache refresh plus two scrapes. Classic quantiles aggregate bucket rates
with `sum by (le, route)` (or `le` for the one fixed assistant operation/provider).
Sparse local traffic makes estimated quantiles coarse, not exact request timings.

For a slow/failed assistant request, find the relevant time window and outcome
in Grafana, compare API and outbound latency with gateway request latency, then
copy that response's `X-Request-ID` from browser developer tools. Filter API,
internal gateway and mock logs using the commands below. Metrics show aggregate
symptoms; the ID joins completion records for that individual call. The outbound
outcome distinguishes timeout, cancellation, rate limiting and rejected output.

## Trust boundaries

The browser does not generate the canonical ID. APISIX **replaces every
browser-supplied ID**, including syntactically valid values, with a UUID using
APISIX 3.14.1's `request-id` plugin. Browser code can read `X-Request-ID` from the
same-origin API response in developer tools, including authentication errors.
No frontend state or token handling is needed.

Rewrite order is explicit: pre-function override **13000**, request-id **12015**,
OIDC **2599**, proxy-rewrite **1008**. The pre-function clears browser IDs before
the plugin generates one. Existing identity sanitization still runs before OIDC;
proxy-rewrite retains the canonical ID while removing cookies. API prefix
rewriting does not remove the header.

Go accepts exactly one ID containing 1–64 ASCII letters, digits, `_`, or `-`.
Missing, duplicate, invalid, or oversized values get a cryptographically random
128-bit hex fallback. Go stores the ID in request context and returns it in the
response header. A valid direct-to-Go ID is only a correlation value: it confers
no authentication or authorization, and is not proof that APISIX was traversed.
The existing direct-access authentication/network-isolation limitations remain.

The assistant client forwards the context ID to the internal gateway. The
internal guard checks the existing service credential **before** accepting a
propagated ID. After authentication it applies the same character/length policy;
an invalid or missing value is cleared and replaced by the request-id plugin.
Rejected service credentials short-circuit before ID handling and may have no
response ID. Reserved-namespace denial and the gateway's signed-out page also
short-circuit before generation.

APISIX's AI driver uses configured headers rather than automatically forwarding
incoming headers. A `before_proxy` post-function at priority 13000 copies the
picked provider configuration for this request, then adds the ID to its header
map before ai-proxy executes. Shared route configuration is never mutated. The
service credential and browser cookies remain stripped; the fake provider
credential remains gateway-owned. The mock accepts and logs the same bounded ID.
Neither mock responses, deterministic scenarios, usage behavior, nor retries
change.

Pinned implementation references: [request-id](https://github.com/apache/apisix/blob/3.14.1/apisix/plugins/request-id.lua),
[AI proxy base](https://github.com/apache/apisix/blob/3.14.1/apisix/plugins/ai-proxy/base.lua),
and [OpenAI driver](https://github.com/apache/apisix/blob/3.14.1/apisix/plugins/ai-drivers/openai-base.lua).
The isolated gateway verification checks the resulting execution order and
header propagation, including simultaneous calls with distinct IDs.

## Events and fields

API and mock processes use standard-library `log/slog` JSON on stderr.

| Event | Fields beyond slog's time/level/msg |
| --- | --- |
| `request_completed` | `component` (`api` or `mock`), `request_id`, `method`, `route`, `status`, `duration_ms`, `error_category` |
| `assistant_call_completed` | `request_id`, fixed `operation=shopping_completion`, fixed `provider=mock`, `duration_ms`, `status`, `outcome` |
| `server_starting`, `server_stopping`, `server_stopped` | `component` |
| `process_failed` | fixed `error_category=startup_or_server_failure` |

Each incoming request has one completion event, including stack unwinding. API routes use Chi
route templates, such as `/products/{id}`; unmatched requests and authentication
rejections before routing use `unmatched`. Mock routes use fixed ServeMux
patterns. Completion error categories are bounded status-based values: `none`,
`request_rejected`, `rate_limited`, `server_error`, and `interrupted` (a handler did not complete). They do not expose internal
error strings. Status includes implicit 200 responses, including empty handlers;
Chi's response wrapper preserves supported flushing, hijacking, push, and
stream-copy capabilities and supports `Unwrap`. No panic recovery is added. An interrupted handler with no written status records
`status=0` rather than claiming a successful response; panic values are not
selected for the completion event.

The outbound event records HTTP status when known (`0` means no response
headers), and a bounded outcome: `success`, `timeout`, `cancelled`,
`rate_limited`, `invalid_provider_output`, `provider_unavailable`,
`invalid_input`, or `payload_too_large`. HTTP 408/504 and local deadline failures
are timeouts; HTTP 429 is rate limiting. Other non-200 responses and transport
failures are provider failures. Invalid/oversized output is rejected. No raw
upstream error is logged. API and outbound events represent different boundaries;
handlers do not separately log the same failure.

Gateway access events use `event=gateway_request_completed`, `request_id`,
`method`, fixed `route`, `status`, `upstream_status`, and `duration_s`. A local
Nginx map classifies routes as `health`, `backend`, `authentication`, `internal-ai`,
`internal-deny`, or `frontend`. It examines the request URI but emits only these
fixed labels; it never emits the URI. This avoids depending on an unavailable
Nginx `$route_id` variable. `upstream_status` is a string and may be `-`, especially
for ai-proxy, which bypasses Nginx's upstream machinery. Use `status` and the Go
outbound event for AI results. Access logs are buffered, with a three-second
flush interval.

The explicit JSON access format replaces the default format. It excludes request
lines, paths, queries, identity, headers, cookies, and bodies. Authentication and
OIDC callback paths use the same format with the fixed `authentication` label;
authorization codes and state are never selected as access-log fields.

**Audit scope:** the pinned Docker image links APISIX `access.log` to stdout and
`error.log` to stderr. Existing `error_log_level: crit` is retained because OIDC
and provider errors at more verbose levels can include sensitive details. The
verification injects a synthetic callback code/state and checks captured gateway
and mock stdout/stderr for those values, credentials, cookies, and prompts.
This demonstrates the exercised paths, not general sanitization of Nginx critical
errors, all possible OIDC failure paths, Keycloak logs, Docker/build diagnostics, or Go's
standard HTTP server panic diagnostics. Do not enable debug/error verbosity with
real sessions or claim those logs are sanitized.

Example sanitized events for a rate-limited assistant call (timestamps omitted):

```json
{"event":"gateway_request_completed","request_id":"b02b4790-0cfe-48d0-a9ae-0758f1705783","method":"POST","route":"backend","status":429,"upstream_status":"429","duration_s":0.012}
{"level":"INFO","msg":"assistant_call_completed","request_id":"b02b4790-0cfe-48d0-a9ae-0758f1705783","operation":"shopping_completion","provider":"mock","duration_ms":10.2,"status":429,"outcome":"rate_limited"}
{"level":"INFO","msg":"request_completed","component":"api","request_id":"b02b4790-0cfe-48d0-a9ae-0758f1705783","method":"POST","route":"/assistant/messages","status":429,"duration_ms":11.1,"error_category":"rate_limited"}
{"event":"gateway_request_completed","request_id":"b02b4790-0cfe-48d0-a9ae-0758f1705783","method":"POST","route":"internal-ai","status":429,"upstream_status":"-","duration_s":0.009}
{"level":"INFO","msg":"request_completed","component":"mock","request_id":"b02b4790-0cfe-48d0-a9ae-0758f1705783","method":"POST","route":"POST /v1/chat/completions","status":429,"duration_ms":0.2,"error_category":"rate_limited"}
```

## Follow and correlate

Run the API with its normal documented environment; optionally capture stderr in
a private local file outside Git:

```sh
umask 077
cd backend
go run ./cmd/api 2> /tmp/cartside-api.jsonl
```

Follow gateway/mock logs with `docker compose logs -f apisix mock-ai`, and API
logs with `tail -f /tmp/cartside-api.jsonl`. To locate a failed assistant request,
copy its response's `X-Request-ID` from browser developer tools, then filter both
sources (requires `jq`):

```sh
request_id=b02b4790-0cfe-48d0-a9ae-0758f1705783
jq -c --arg id "$request_id" 'select(.request_id == $id)' /tmp/cartside-api.jsonl
docker compose logs --no-log-prefix apisix mock-ai |
  jq -Rc --arg id "$request_id" 'fromjson? | select(.request_id == $id)'
```

Find the API completion's route/status, then the outbound outcome and status,
then the internal gateway and mock completions with that ID. A timeout can leave
a mock completion after the gateway response; allow for cancellation and log
buffering. The mock may record an implicit 200 after cancellation even though the
gateway returned 504; it records the handler's status, not proof of delivery.
Remove local log files when finished.

## Verification

```sh
cd backend
gofmt -l cmd internal
go vet ./...
go test ./...
go test -race ./...
go build ./...
cd ..
python3 -m venv /tmp/cartside-verify-venv
/tmp/cartside-verify-venv/bin/pip install -r scripts/requirements-ci.txt
/tmp/cartside-verify-venv/bin/python scripts/validate-config.py
/tmp/cartside-verify-venv/bin/python scripts/verify-ai.py
```

Unit tests capture JSON in memory, check redaction and route templates, fallback
and supplied IDs, implicit/explicit status, concurrent context isolation, and
outbound cancellation/timeout/rate-limit/malformed/provider-error outcomes.

`verify-ai.py` never reads `.env` or uses developer volumes. It creates a random
Compose project, validates Compose and APISIX configuration, builds the mock,
and removes its resources afterward. It preserves the original credential,
namespace, size, scenario, timeout, and no-retry checks. It also starts a disposable
host Go/Chi transport fixture through the public health route and a test-only
assistant route, verifies gateway-generated response IDs and Go/outbound/mock
correlation for all fault scenarios, checks supplied/invalid/oversized IDs and
concurrent internal calls, and checks captured logs for sensitive fixtures.
The fixture exercises the real client and gateway/mock path; it does **not**
exercise browser login, production assistant authorization, catalog grounding,
or persistence. The production OIDC route configuration is unchanged by the
fixture. `scripts/e2e.py` additionally captures private API/gateway/mock logs and verifies
ordinary catalog correlation, both authenticated browser assistant calls, and
absence of known credentials, identity, and prompt fixtures. It requires Node 24;
see [the E2E guide](e2e.md).

Verified on 2026-10-23: Go formatting/vet/unit/race/build; configuration parsing;
isolated Compose startup and `apisix test`; all gateway correlation/fault and
concurrency checks; six PostgreSQL integration suites with race detection; and
all five Chromium shopping tests with the new authenticated correlation and
redaction assertions. The browser run used an isolated Node 24.15.0 executable
because the shell default was Node 22. No frontend source changed, so frontend
lint/type-check/unit/build checks were not rerun. Both runners removed their
disposable containers and volumes; developer data was preserved. No general
critical-error-log sanitization or production readiness is claimed.

### Metrics milestone verification

Run the isolated monitoring check with Python dependencies from
`scripts/requirements-ci.txt` installed:

```sh
python3 -m venv /tmp/cartside-observability-venv
/tmp/cartside-observability-venv/bin/pip install -r scripts/requirements-ci.txt
/tmp/cartside-observability-venv/bin/python scripts/verify-observability.py
# Optional: with frontend npm dependencies and Playwright Chromium installed:
/tmp/cartside-observability-venv/bin/python scripts/verify-observability.py --browser
```

The runner ignores `.env`, allocates ephemeral localhost ports and a random Compose
project, starts a host Go/Chi transport fixture, and cleans up only its own
containers/volumes. It validates Prometheus with `promtool`, APISIX with
`apisix test`, target health, success/provider-failure counters and histogram
counts, every overview query, authenticated Grafana dashboard provisioning, and
known sensitive values in Go and gateway exposition. It exercises transport
boundaries, not production OIDC authorization or shopping persistence. Unit tests
cover isolated registries, route templates, bounded unknown paths, in-flight
counts, interrupted handlers, and outbound failure/cancellation outcomes.

Verified for this milestone on 2026-10-23: Go formatting, vet, unit/race tests and
build; all changed/new YAML/JSON parsing; ordinary Compose configuration without
Grafana credentials; `promtool check config`; `apisix test`; both scrape targets
UP; ordinary, successful assistant and controlled provider-failure traffic;
expected counters, histogram counts and positive duration sums; every dashboard
query returning series; authenticated Grafana provisioning; and sensitive fixture
checks on full Go/APISIX exposition. The existing isolated AI gateway regression
suite passed authentication, namespace/model/size restrictions, correlation,
concurrency, no-retry and all provider-fault scenarios. A smoke run of the actual
API verified liveness, metrics startup, SIGTERM and closure of both listeners.
Frontend source did not change; frontend build/unit checks and shopping/database
integration suites were not rerun. All disposable stack resources were removed;
developer services and volumes were preserved.

The `--browser` run also passed Chromium sign-in and rendered overview panels
(scrape health, assistant failures and gateway latency). This was a browser smoke
check, not a pixel review, accessibility audit, or cross-browser test. There were
no remaining verification blockers. The committed runner waits for login navigation and completes browser
verification before removing the isolated stack.
