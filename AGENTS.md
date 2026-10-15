# Cartside

## Project
- Read `README.md` for architecture, scope, and current implementation status.
- Stack: React with TypeScript, Go with Chi, PostgreSQL, Apache APISIX, and Keycloak.
- Keep one repository and a modular Go monolith. Add modules as features require them.
- React and Go run on the host; local infrastructure runs in Docker Compose.

## Boundaries
- APISIX manages OIDC sessions with Keycloak and routes application and AI traffic.
- Go owns business rules, resource authorization, and application data. Trust only verified gateway identity; never client-supplied identity headers.
- Modules use each other's operations rather than directly modifying each other's tables.
- AI suggestions never change a cart without explicit customer acceptance. Validate products, quantities, and prices in Go.
- Checkout must preserve transaction atomicity, safe retries, and protection against concurrent cart changes.

## Working style
- Implement only the requested milestone. Avoid speculative abstractions, dependencies, and unrelated refactors.
- Inspect existing code and instructions before editing; preserve unrelated work.
- Keep secrets out of Git, logs, and browser bundles. Document configuration with safe placeholders in `.env.example`.
- Update the README when setup or behavior changes. Distinguish working features from planned work.
- Do not commit, push, or deploy unless requested.

## Verification
- For Go changes, run formatting, `go vet ./...`, `go test ./...`, and `go build ./...` from the Go module directory.
- For frontend changes, run available lint, type-check, test, and build scripts relevant to the change.
- Add focused tests for business rules and meaningful failure cases; avoid tests that merely mirror trivial code.
- Verify new runtime behavior directly when practical. Report checks performed and any blockers accurately.
