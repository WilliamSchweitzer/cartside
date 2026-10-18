# Non-AI shopping verification

The full-stack suite is explicit and is not part of ordinary CI. It needs Docker
with Compose v2, Go 1.24, Node **24.15 or later within 24.x**, Python 3, and Chromium.
From the repository root:

```sh
python3 -m venv /tmp/cartside-e2e-python
/tmp/cartside-e2e-python/bin/pip install -r scripts/requirements-ci.txt
cd frontend
npm ci
npx playwright install chromium
cd ..
/tmp/cartside-e2e-python/bin/python scripts/e2e.py
```

The runner checks prerequisites, creates a randomly named Compose project with
private temporary configuration and fresh volumes, imports two disposable users,
chooses unused ports, starts host Go/Vite processes, migrates/seeds its application
database, and runs five existing database suites against separate empty databases.
It then invokes the five Playwright tests with one Chromium worker. It does not
read `.env`, touch developer users/volumes, stop existing services, or alter CI.
A port claim between allocation and startup fails the run; rerun to choose new
ports. Docker must support `host.docker.internal` (Compose adds the host gateway).

Normal completion, failure, and Ctrl-C stop only the runner's host process groups,
remove only its Compose project's volumes, and delete private files. If the
runner is forcibly killed, find its `cartside-e2e-*` project with `docker compose ls`
and remove only that project's resources. Never use global Docker prune commands.

Secrets are random per run and are not printed. Container logging, browser traces,
screenshots, and video are disabled. The custom reporter prints only test titles
and statuses; the runner suppresses subprocess errors to avoid recording login
call arguments or OIDC responses. A failed stage is reported without its raw
output. Do not enable debug logging or publish raw browser/network artifacts.

## Coverage and limits

The browser suite covers sign-in and signed-out API enforcement, pagination,
`broccxli` → Broccoli Crowns/Florets, additions/quantity/removal/totals/reload,
separate users, explicit keyboard review/confirmation, a 375px overflow check,
logout, rapid double click, a real committed response dropped in transit, exact
same-key/body replay after reload and logout/sign-in, identity isolation, failed
post-success cart refresh, and price/availability/version rejections after an
uncertain attempt. Database tests cover atomicity, snapshots, concurrency, and
backend identity isolation. Frontend tests cover storage failures/malformed
records, timeouts, unmarked gateway responses, and late identity responses.

The logout/sign-in browser case verifies recovery through a new session; it does
not simulate passive refresh-token expiry. Keyboard and viewport assertions are
basic checks, not a screen-reader or broad device/browser audit. Confirmation
survives failed cart refresh but is not order history and is lost after a resolved
page reload. Recovery storage is scoped to the tab and origin. Cross-tab/device
UI coordination, payments, inventory integration, delivery, and AI remain outside
this milestone.

## Recovery contract

An uncertain response retains the exact stored key and body and blocks mutations.
Only an explicit retry sends that request. For the five cart/precondition conflicts,
Go checks for a matching committed order before and after taking the cart lock.
A rejection after these lookups carries
`X-Cartside-Checkout-Rejection: no-matching-order` with its JSON 409 response.
The client requires the marker, JSON content type, and a known conflict code
before clearing recovery data, including on retries. It refreshes the cart and
requires a new explicit review before a new attempt can be created. The marker is
an application response contract over the trusted same-origin gateway, not a
client identity assertion or cryptographic proof; gateway policy must not synthesize it.

Unmarked responses (including gateway 400/403), malformed confirmations, timeouts,
401, and exceptional idempotency conflicts retain recovery. Renew a rejected
session and retry; for persistent invalid-input/security/idempotency failures,
restore the service/configuration or investigate the original attempt rather than
clearing storage or changing its body/key. A successful retry shows the original
confirmation. A marked rejected retry refreshes/reviews; it never automatically
places a replacement order. Storage read failures/corruption block requests;
write/verification failures before submission send nothing. Do not clear an
unresolved record to bypass a lock.

## Current evidence (2026-10-18)

All five Playwright tests passed through the real gateway, identity provider,
frontend, Go backend, and PostgreSQL on a fresh isolated stack. All five existing
PostgreSQL integration suites passed with `-race`, each against its own empty
database. Go formatting/vet/unit/race/build, frontend lint/application-and-E2E
type-check/46 unit tests/build, JSON/YAML validation, and diff whitespace checks
passed using Go 1.24.0 and Node 24.15.0. Both verification projects were cleaned
up, including failed-run resources; no developer configuration/data was changed.

The initial browser run found an ambiguous quantity locator and an order-count
assertion dependent on another test's success. Those test defects were corrected,
and the complete isolated run passed. No remaining reproducible application
defects were found in the exercised flow. The coverage limitations above remain;
no passive session-expiry or screen-reader verification is claimed.
