# Deployment packaging verification — 2026-10-24

Scope: local disposable production-style HTTPS packaging before cloud provisioning.
No cloud resources, public DNS, public certificates or public application deployment
were created. Developer containers, users and volumes were preserved. No pruning
was performed. Self-signed certificates and test identities existed only in the
random verifier project. Source: base `57e55c6229b3d77989fde4bc3fbfd1702502ba77`
plus the packaging verification changes containing this record.

## Fixes and reproducibility

- Both Go builders use Go 1.27.3, including the preserved scratch/non-root mock
  runtime. The module's Go 1.24 declaration is a compatibility floor. The reviewed
  [Go release history](https://go.dev/doc/devel/release#go1.27.3) identifies the
  crypto/x509 security update.
- API database credentials use `cartside_runtime`; migration/seed tooling uses
  `cartside_migrate`; PostgreSQL bootstrap and Keycloak remain separate.
  Explicit, transactional provisioning supports fresh and existing application
  tables without deleting rows. See [permissions and upgrade procedure](deployment.md#fresh-database-and-identity-provisioning),
  including the product-ID column privilege required for PostgreSQL row locking.
- The runner initializes PostgreSQL first and runs bootstrap jobs explicitly,
  avoiding Compose wait-mode failure on an independently completed bootstrap job.
- Chromium resolves `.localhost` automatically, but Node returned `ENOTFOUND`.
  A disposable Node preload maps only the two test hostnames, through callback and
  promise DNS APIs. Separate probes verified both APIs and that other names retain
  normal resolution. No system DNS or production OIDC/TLS settings changed.
- The browser suite explicitly checks hashed static JS/CSS, all session cookie
  flags and foreign-origin CSRF rejection. The runner checks migration ownership,
  runtime migration denial, internal discovery, ports, restart persistence,
  monitoring, shutdown and cleanup. It records resource samples without secrets.

Reproduce with native ARM64 Docker, Python plus `scripts/requirements-ci.txt`,
Node 24, frontend npm dependencies and Playwright Chromium:

```sh
IMAGE_TAG=verify-20261024-go1273 docker compose \
  --env-file infra/deploy/.env.example -p cartside-package-build \
  -f compose.deploy.yaml build
CARTSIDE_DEPLOY_IMAGE_TAG=verify-20261024-go1273 python3 scripts/verify-deployment.py
```

Use a new image tag for a new build; the tag above identifies this verification's
local images. Example environment values are used only for Compose build parsing;
the runner creates independent random secrets and never sources developer `.env`.

## Executed checks

| Check | Evidence |
| --- | --- |
| Native ARM64 build/execution | macOS ARM64 host; Docker Desktop 4.95.0 / Engine 29.8.2, linux/arm64; all three application images built and executed natively; no cross-build or emulation |
| Storage preflight | Host about 83 GiB free; Docker filesystem about 206 GiB free before building; existing volumes left intact |
| Image architecture/digests | All required/optional manifests include linux/arm64; [application/runtime/base image record](deployment-images.json) includes exact digests and configuration hashes; images stayed local |
| Go checks | Host Go 1.27.2: formatting, `go vet ./...`, `go test ./...`, `go build ./...` passed; Docker Go 1.27.3 built API/migrate/seed and mock |
| Frontend checks | Node 24.15.0: lint, application/E2E type checks, 86 unit tests and production build passed |
| Configuration | Compose validation, deployment boundary checks, JSON/YAML parsing, shell syntax, Python compilation and diff whitespace checks passed |
| Monitoring configuration | Pinned promtool configuration/rule checks and five-rule alert test suite passed |
| Database bootstrap/migration | Fresh schema migrated/seeded with migration role through version 4; repeated provisioning and migration succeeded after deliberately restoring bootstrap ownership of existing tables; 100 products preserved |
| Restricted runtime | Live API sessions used runtime role; runtime migration attempt failed; no schema/temp creation, migration-table read, price update, order deletion, migration-role membership or Keycloak database connection privilege |
| Direct negative SQL probes | Under runtime session authorization, nine probes rejected table/temp creation, migration-metadata reads, catalog price/name writes, order deletion/item mutation, and migration/Keycloak role assumption |
| HTTPS browser suite | Six Chromium scenarios passed: sign-in/search/pagination/static assets, Secure/HttpOnly/SameSite=Lax cookies, CSRF rejection, cart operations/isolation, mock suggestion acceptance/decline, checkout/replay/conflicts, logout and identity-host boundaries |
| OIDC trust | Internal discovery returned canonical HTTPS issuer/browser authorization URL and private HTTP token endpoint; real browser login/logout passed with issuer, audience, signature, PKCE, nonce and production TLS settings retained |
| Network/health | All seven default long-running services healthy; only edge HTTPS published to loopback; database/API/mock/metrics/admin ports unpublished; identity `/admin/`, master realm, health and metrics returned 404 |
| Restart persistence | All default services restarted; application row counts persisted and all six browser scenarios passed again, including sign-in with persisted Keycloak users |
| Optional monitoring runtime | Both services healthy and unpublished; Grafana database healthy; Prometheus API/gateway targets both up |
| Shutdown | 3.16 seconds; Go logged `server_stopping` and `server_stopped` at restart and final stop; Keycloak exit 143 (SIGTERM), every other service/job exit 0; no SIGKILL or OOM |
| Cleanup/preservation | Random project's containers and volumes absent after cleanup; four pre-existing developer services still healthy; their volumes/users/data were not removed or reused |

## Measured resources

Docker VM: 8 logical CPUs, 4,106,604,544 bytes RAM (about 3.82 GiB); four developer
services remained active outside the measured project. `docker stats --no-stream`
was sampled roughly every 3 seconds (command duration plus 2-second interval).
The table sums **per-service sampled maxima**, a conservative envelope rather than
a simultaneous peak. CPU 100% represents one logical core. Browser-load includes
the restart/recovery interval and both serial browser runs. Monitoring includes
its startup and final shutdown, so it is not a steady-state estimate.

| Phase | Memory envelope (MiB) | CPU envelope |
| --- | ---: | ---: |
| startup | 864.9 | 635.7% |
| idle | 861.1 | 18.5% |
| browser-load | 927.3 | 286.6% |
| monitoring | 1502.2 | 150.7% |

Keycloak sampled startup CPU reached 565.83% and browser/restart memory 690.1 MiB.
Grafana sampled startup memory reached 697.9 MiB. These are observed container
samples, not reserved memory, actual instantaneous total peaks or production capacity.

Before builds, host free space was about 83 GiB and Docker filesystem free space
about 206 GiB. After builds, final-run host free space changed from 76.67 GiB to
76.41 GiB. Docker's global image/cache accounting was 5.982 GB / 5.326 GB; it
includes unrelated cached images and is not this project's exclusive footprint.
The three application images total 339,218,553 reported bytes before shared-layer
accounting. No images were published or exported as a release archive.

Project-specific allocated disk measurements (`du -sk` on read-only volume mounts):

| Storage | Idle | After browser/restart/monitoring/shutdown |
| --- | ---: | ---: |
| PostgreSQL data | 71,744 KiB | 77,716 KiB |
| PostgreSQL socket volume | 8 KiB | 4 KiB |
| Grafana data | Not started | 1,636 KiB |
| Prometheus data | Not started | 64 KiB |
| Container writable layers | 177,278,976 bytes | 177,758,208 bytes |

All measured test volumes were removed afterward. These short-run measurements
exclude Docker VM overhead, build transients, separately stored container logs,
future data growth and backup storage. They cannot size a production VM.

## Limits and remaining launch gates

These are small, serial Chromium demo flows, not a load/capacity benchmark. Resource
sampling can miss transient peaks; Docker's memory accounting is not total VM RAM.
No screen-reader, broader browser/device, passive token-expiry, sustained traffic,
active-request drain under long-running load, or real-provider compatibility claim
is made. This verification does not test a public certificate chain or renewal,
Oracle VM networking, cloud quotas, or target-host performance.

Public launch remains blocked on tested application/Keycloak backup and restore,
off-VM encrypted retention and configuration recovery, rollback rehearsal with
RPO/RTO, registration/traffic abuse and retention decisions, secret rotation/private
admin recovery, release artifact retention/dependency review, target-host sizing,
firewall/DNS/certificate delivery and renewal, and separate provisioning/publication
authorization. No backup/restore or public-launch gate is marked passed here.
