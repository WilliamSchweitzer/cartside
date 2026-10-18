# First AI milestone: deterministic local protocol testing

Implemented: an internal Go shopping assistant → APISIX 3.14.1 `ai-proxy` →
local Go mock service. No external model calls, browser AI features, public
assistant endpoint, persistence, or cart/order mutations. Go/React run on the
host; `backend/cmd/mock-ai` runs in Compose.

## Catalog-grounded internal operation

`shoppingassistant.New(catalogRepository, client).Suggest(ctx, request)` accepts
one request of 1–256 bytes. Construct the client explicitly with
`shoppingassistant.NewClient(gatewayURL, serviceCredential)`. Safe example names
are `CARTSIDE_AI_GATEWAY_URL` and `CARTSIDE_AI_SERVICE_KEY` in `.env.example`;
`cmd/api` does not consume them yet. Never supply these to Vite. The URL must
have the exact `/internal/ai/chat/completions` path with no credentials, query,
or fragment. HTTP is for local development; use HTTPS beyond a trusted host.
The client has no cookie jar, redirects, identity input, or automatic retries.
It bounds requests to 8192 bytes, responses to 2048 bytes, headers to 4096 bytes,
and each HTTP call to two seconds; the entire operation has a five-second
context deadline. No limit changes were needed.

Go normalizes whitespace and case, then uses this explicit mapping in
`internal/shoppingassistant/assistant.go`:

| Exact demo request | Catalog search terms |
| --- | --- |
| breakfast for two | oats, milk, bananas |
| vegetarian dinner | rice, beans, broccoli |
| snacks for a picnic | apples, crackers, cheese |

This is deterministic demo behavior, not language understanding. Each term uses
catalog `List` with limit 2, offset 0; unavailable products are omitted and IDs
are deduplicated, yielding at most six candidates. No catalog-wide query is
used. Unsupported requests and empty candidate sets return local clarification
with an empty suggestions array and no model call. Names/descriptions do not
establish dietary or allergen suitability; even the vegetarian demo offers no
verified suitability. No user identity, raw customer request, cookies, tokens,
cart, order history, full catalog, or product prices go to the provider.

The fixed non-streaming Chat Completions request contains one user message whose
content is JSON:

```json
{"version":1,"scenario":"breakfast for two","candidates":[{"product_id":"<catalog UUID>","name":"Rolled Oats","package_label":"1 bag"}]}
```

Candidate names are bounded to 160 bytes and packages to 100. The mock selects
the first five unique supplied candidates in order, quantity 1, with a short
fixed reason; it contains no fixture UUIDs. The completion message content is
itself a JSON object:

```json
{"version":1,"text":"SIMULATED: Demo catalog suggestions. Dietary and allergen suitability is unverified.","suggestions":[{"product_id":"<supplied UUID>","quantity":1,"reason":"Demo scenario catalog candidate."}]}
```

Go treats the completion envelope and content as untrusted: unknown/duplicate
fields, nulls, trailing content, wrong types, wrong model/version/role/finish,
invalid IDs, duplicate selections, more than five suggestions, quantities outside
integer 1–99, text over 400 bytes and reasons over 120 bytes are rejected. The
client allows only candidate IDs. Catalog `Get` re-reads every selection; missing
or unavailable products invalidate the whole result. Names, packages, integer
cent prices, currency and availability come exclusively from these current
PostgreSQL reads. Changed prices/names are refreshed, not taken from the model.
Reads are current per product, not an atomic multi-product snapshot; future
acceptance must validate again through cart operations.

The future HTTP adapter will accept `{"request":"breakfast for two"}` (one
1–256 byte request string, no conversation history) and return this business
result. No route or HTTP status mapping is implemented yet:

```json
{"version":1,"provider":"simulated: cartside-mock-v1","text":"SIMULATED: ...","suggestions":[{"product_id":"<UUID>","quantity":1,"reason":"Demo scenario catalog candidate.","name":"Rolled Oats","package_label":"1 bag","price_cents":399,"currency":"USD","available":true}]}
```

Clarification uses the same contract with `suggestions: []`. Safe typed error
codes are `invalid_input`, `payload_too_large`, `timeout`, `rate_limited`,
`provider_unavailable`, `invalid_provider_output`, `catalog_unavailable`, and
`catalog_changed`; errors never include URLs, credentials, prompts or upstream
bodies. Future endpoints must add verified authentication, request limits and
per-user controls before exposure. Suggestions cannot invoke mutations.

```mermaid
flowchart LR
    C[Host client] -->|Service key /internal/ai/chat/completions| A[APISIX ai-proxy]
    A -->|Fixed fake provider key /v1/chat/completions| M[Go mock in Compose]
```

## Pinned plugin decision

The existing `apache/apisix:3.14.1-debian` image is unchanged. Consulted sources:

- [APISIX 3.14 ai-proxy documentation](https://apisix.apache.org/docs/apisix/3.14/plugins/ai-proxy/)
- [3.14.1 plugin schema](https://github.com/apache/apisix/blob/3.14.1/apisix/plugins/ai-proxy/schema.lua)
- [3.14.1 single-provider plugin](https://github.com/apache/apisix/blob/3.14.1/apisix/plugins/ai-proxy.lua)
- [3.14.1 request/retry implementation](https://github.com/apache/apisix/blob/3.14.1/apisix/plugins/ai-proxy/base.lua)
- [3.14.1 OpenAI-compatible driver](https://github.com/apache/apisix/blob/3.14.1/apisix/plugins/ai-drivers/openai-compatible.lua)
- [3.14.1 OpenAI base driver](https://github.com/apache/apisix/blob/3.14.1/apisix/plugins/ai-drivers/openai-base.lua)

`provider: openai-compatible` supports `override.endpoint`. It is fixed to
`http://mock-ai:8090/v1/chat/completions`; this is an actual AI plugin route,
with no ordinary proxy upstream. The wire contract is the non-streaming OpenAI
Chat Completions shape. `options` also pins `model` and `stream`.
The plugin constructs provider headers from configuration, rather than forwarding
client headers. It reads `choices[].message.content` and tolerates missing
`usage`; the mock omits usage entirely rather than inventing token accounting.

Both `ai-proxy` and its runtime dependency `prometheus` are explicitly enabled
alongside all existing plugins. The pinned plugin unconditionally invokes
Prometheus LLM counters even without a route metrics plugin; excluding Prometheus
causes inference to fail. No metrics port is published to the host.

## Boundary and limits

`POST /internal/ai/chat/completions` requires an independent
`X-Cartside-AI-Key` from `CARTSIDE_AI_SERVICE_KEY`. A rewrite-phase guard checks it
before the AI plugin runs. Missing/wrong credentials return 401, including with
a valid Keycloak storefront cookie. The entire `/internal` namespace is reserved
at higher priority than the frontend catch-all; other paths return 404.
Existing OIDC, API, health, and shopping routes retain their policies.

The key is removed after authentication; Authorization and Cookie are cleared.
The provider gets only the configured fake Authorization value
`Bearer cartside-local-fake-provider-only`, which is intentionally public and
has no value outside the mock. The mock rejects forwarded service credentials or
cookies. It has no host port mapping. Keep the service key in ignored `.env`;
never put it in Vite configuration, a URL, or browser storage. Local Docker
administrators can inspect container environments. This is a service-authenticated
local route, not a public assistant endpoint or a production network boundary.

Only `model`, `messages`, and optional `stream: false` are accepted. The model
must be `cartside-mock-v1`; messages must contain 1–16 nonempty string messages
with roles `system`, `user`, or `assistant`. Extra fields, query parameters,
streaming, and alternate models are rejected before the provider. URLs in ordinary
message text remain text; they do not affect routing.

The guard bounds the JSON body to 8 KiB; Go independently bounds it to 8 KiB.
Nginx's existing body buffering/default 1 MiB ceiling and 60-second client body
inactivity timeout apply while receiving the request, before the 8 KiB guard.
The provider uses a 4 KiB header limit, 1-second read-header, 2-second read,
5-second write, and 10-second idle timeouts. Successful JSON responses are capped
at 2 KiB by the provider, with short fixed error/malformed responses. No response
echoes the prompt. These response bounds rely on this fixed, trusted mock.
The pinned AI driver buffers provider responses without a configurable response
size cap; replacing the mock requires an independent upstream response limit.

APISIX's AI timeout is 1,000 ms per socket operation; this is not a universal
end-to-end deadline. The fixed mock emits one small response without incremental
chunks, so its 3-second delay reliably produces 504 in approximately one second.
`keepalive: false` closes each inference connection. Single-provider `ai-proxy`
passes no retry callback to its base driver and makes one attempt; no fallback or
Nginx upstream retries are used. Provider invocation counters verify this for
429, 500, malformed output, and timeout scenarios.

APISIX access logs and AI payload/summary logging are disabled; error logging is
`crit` (the pinned driver can otherwise log malformed response bodies). The Go
mock logs neither requests nor responses. The Dockerfile-specific ignore file
excludes `.env` and unrelated files from its build context.

## Supported deterministic scenarios

For compatibility, bare-text smoke requests retain the original text-only demo contract.
The latest user message is trimmed and matched case-insensitively. Earlier
messages are validated but do not affect the answer. Every answer starts with
`SIMULATED:` and all completion metadata is deterministic.

| Latest user message | Simulated content |
| --- | --- |
| `breakfast for two` | Oats, milk, bananas |
| `vegetarian dinner` | Rice, beans, broccoli |
| `snacks for a picnic` | Apples, crackers, cheese |
| Anything else | Clarifies meal, dietary needs, and number of people |

Faults are **process configuration**, never customer text or client headers.
The ordinary Compose service has no fault settings. Disposable test services set
`MOCK_TEST_MODE=1` and select `MOCK_SCENARIO`:

| Scenario | Behavior |
| --- | --- |
| `normal` (default) | Deterministic Chat Completions response |
| `malformed` | HTTP 200 with deliberately invalid JSON |
| `rate-limit` | HTTP 429 |
| `server-error` | HTTP 500 |
| `delay` | Waits three seconds before responding, unless canceled |

Non-normal scenarios fail startup outside test mode. Unknown scenarios also fail
startup. `GET /health` remains a fast 200 in every mode and does not invoke
inference. Only test mode exposes `GET /test/stats` with an atomic inference call
count; neither health nor stats is routed through APISIX. Verification reads
stats by executing the probe binary inside the provider container.

## Local setup and exact verification commands

Use the README setup Python block to generate the new independent 32-byte hex
key in `.env`, preserving existing secrets. Do not copy the template over an
existing `.env`. Then:

```sh
docker compose config --quiet
docker compose up -d --build --wait
```

For a manual host-client request, the following reads the trusted local `.env`
without displaying the key or putting it in command-line arguments. Run it from
the repository root; it prints only the intentionally simulated answer.

```sh
python3 - <<'PY'
import json
from pathlib import Path
import urllib.request
values = dict(line.split('=', 1) for line in Path('.env').read_text().splitlines()
              if line and not line.startswith('#') and '=' in line)
body = json.dumps({'model': 'cartside-mock-v1', 'stream': False,
                   'messages': [{'role': 'user', 'content': 'breakfast for two'}]}).encode()
request = urllib.request.Request('http://127.0.0.1:9080/internal/ai/chat/completions',
    data=body, headers={'Content-Type': 'application/json',
                       'X-Cartside-AI-Key': values['CARTSIDE_AI_SERVICE_KEY']})
with urllib.request.urlopen(request, timeout=5) as response:
    print(json.load(response)['choices'][0]['message']['content'])
PY
```

Automated checks (Go 1.24.0, Node 24.15+, Python 3, Docker Compose):

```sh
# From repository root. Creates only a disposable Python environment.
python3 -m venv /tmp/cartside-ai-checks
/tmp/cartside-ai-checks/bin/pip install -r scripts/requirements-ci.txt
/tmp/cartside-ai-checks/bin/python scripts/validate-config.py
/tmp/cartside-ai-checks/bin/python scripts/verify-ai.py
# Includes real Keycloak login, session-only AI denial, zero provider invocations,
# all six DB integration packages, and the existing five shopping browser tests.
/tmp/cartside-ai-checks/bin/python scripts/e2e.py

cd backend
test -z "$(gofmt -l .)"
go vet ./...
go test ./...
go test -race ./...
go build ./...
cd ../frontend
npm ci
npm run lint
npm run typecheck
npm test
npm run build
```

The AI runner uses unique project/image names, an ephemeral gateway port, private
configuration, generated credentials, no developer `.env`, and no persistent
volumes. It validates Compose and the running APISIX configuration, tests the
actual plugin, rejects invalid inputs without inference, and checks one provider
invocation per fault. It tears down its project and local image even on failure.
The shopping runner also uses a unique project with disposable databases/users
and removes that project's containers, volumes, and private configuration.
Neither runner stops existing developer services or changes developer data.

## Compatibility limits

These tests verify this mock's protocol and gateway policies, **not compatibility
with any live model provider**. No provider availability, real model quality,
tokenization, billing, TLS-to-provider behavior, or external error semantics have
been tested. No synthetic usage values are emitted.

In 3.14.1 the AI driver passes malformed JSON through as HTTP 200 rather than
rejecting it. The Go assistant independently validates the completion contract.
For provider 429/5xx it returns the status without preserving the provider's body
or `Retry-After`. The test expects those pinned behaviors. Timeouts are per socket
operation and arbitrary provider response sizes are not capped by this plugin.
None of these observations justify silently changing the pinned gateway version
or claiming real-provider readiness.

## Verification record (2026-10-18)

Passed locally with Go 1.24.0 and Node 24.15.0: formatting, Go vet/unit/race/build,
frontend lint/type-check/46 unit tests/build, configuration parsing, isolated
Compose startup and APISIX configuration validation, live AI authentication/input
limits and all four fault scenarios. The delayed call returned 504 in about one
second; every fault produced exactly one provider invocation. The isolated full
stack passed all five database integration packages and five Chromium shopping
tests, including valid-session-only AI denial with zero provider calls.
Developer configuration and persistent data were preserved. Live model provider
compatibility and remote CI were not exercised.

## Assistant milestone verification

Run the Go commands above for unit/race checks. `scripts/verify-ai.py` additionally
checks a structured candidate-grounded completion through the real gateway.
`scripts/e2e.py` now includes the assistant integration package in its disposable
database loop. For a separately provisioned **empty disposable database**:

```sh
cd backend
CARTSIDE_TEST_DATABASE_URL='<disposable PostgreSQL URL>' go test -tags=integration -race ./internal/shoppingassistant -count=1
```

This test applies migrations, seeds catalog fixtures, checks all three scenarios
against PostgreSQL, and verifies cart/order snapshots remain unchanged. Unit
tests exercise product deletion/unavailability and refreshed catalog metadata,
untrusted content, limits, timeout, rate limits, and single-attempt failures.
No paid provider access is required.

Verification for this milestone (2026-10-18): Go 1.24.0 formatting, vet, unit/race
tests and build passed; configuration parsing and whitespace checks passed. All
six integration packages passed with the race detector against separate fresh
PostgreSQL 17.10 databases. Isolated APISIX/mock verification passed the original
smoke scenarios, structured candidate selection, authentication, request limits,
and malformed/rate-limit/server-error/timeout faults with one provider invocation
each. Developer data was preserved and disposable containers/volumes removed.
Frontend and full browser E2E checks were not rerun for this internal Go change.
