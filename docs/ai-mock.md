# First AI milestone: deterministic local protocol testing

Implemented: a host client → APISIX 3.14.1 `ai-proxy` → local Go mock service.
There are no external model calls, real provider keys, browser AI features,
product IDs, catalog grounding, cart suggestions, or cart mutations. The mock
returns shopping text only. Application Go/React still run on the host; the
standalone infrastructure command `backend/cmd/mock-ai` runs in Compose.

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
# all five DB integration packages, and the existing five shopping browser tests.
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
rejecting it. Future application clients must validate the completion contract.
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
