# Request correlation and structured logs

Cartside uses `X-Request-ID` to correlate requests. This is **request correlation,
not distributed tracing**: there are no spans, trace propagation, sampling,
metrics, or dashboards in this milestone. IDs must never become metric labels.
The existing APISIX Prometheus plugin remains loaded only because the pinned AI
plugin requires it internally; this change adds no metrics infrastructure.

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
