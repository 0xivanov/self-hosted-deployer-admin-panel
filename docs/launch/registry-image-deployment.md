# Registry image deployment

Status: implementation in progress. The portal does not yet create or deploy registry-image projects. Existing ZIP-based static and Node deployments are unchanged.

## Implemented image check

`internal/registryimage` and `cmd/registry-image-check` provide a metadata-only check for Docker Hub and GHCR. They accept an explicit tag or SHA-256 digest, resolve exactly one Linux ARM64 v8/default image, verify manifest and config hashes, and return a platform-specific digest reference. A tag is resolved once, not followed indefinitely.

The check bounds manifests to 2 MiB, config to 1 MiB, layers to 128 descriptors and declared compressed-layer bytes to 512 MiB. These are initial intake constraints, not a published hosting plan. Layer contents and expanded disk use still require verification during import. Exposed TCP ports are suggestions from image metadata; they do not prove which port the application listens on.

Requests use fixed registry/token endpoints and pull-only scope. Optional credentials come from an owner-private JSON file and are not included in results. Token and manifest requests refuse redirects. Config blob redirects permit only HTTPS on public addresses, with no forwarded authorization. DNS results are validated and the selected address is dialed directly. The client does not use environment proxy settings. There is no shared credential/token cache.

Run from the admin-panel repository:

```sh
go run ./cmd/registry-image-check --image docker.io/library/nginx:stable-alpine
go run ./cmd/registry-image-check --image ghcr.io/nginx/nginx-unprivileged:stable-alpine
```

For a private repository, add `--credentials /absolute/private/registry.json`. The file must contain only `username` and `password` (the registry access token), with no group or other access. Do not put credentials in command-line arguments or commit the file. A successful check does not grant cluster pull access or persist credentials.

Reference behavior follows the [Distribution registry API](https://distribution.github.io/distribution/spec/api/). The public GHCR probe uses the image registry documented by the [NGINX unprivileged project](https://github.com/nginx/docker-nginx-unprivileged).

## Evidence collected September 23, 2026

Focused tests cover reference injection, digest tampering, coherent wrong-architecture metadata, missing/ambiguous ARM64 selection, foreign descriptor URLs, size bounds, private/reserved address denial including mapped private IPv4, fixed auth destinations, pull scope and authorization stripping on redirects. No test contacts a private registry.

Read-only probes from the development Mac succeeded for both registries:

| Source | Selected ARM64 digest | Declared compressed layers | Exposed TCP port |
| --- | --- | --- | --- |
| Docker Hub `library/nginx:stable-alpine` | `sha256:3abad30db61dfb2a339ea21acc9f3e6bbe78e291fcff2f835f4510c8fdfcbd83` | 25,966,398 bytes | 80 |
| GHCR `nginx/nginx-unprivileged:stable-alpine` | `sha256:98ae898055c08d3f2738a27d23fe3cbb3e256aa6060239cbaa17b4cd7f3f31ac` | 23,137,320 bytes | 8080 |

These are metadata observations, not runtime qualification, vulnerability scanning, image trust verification or deployment results. No layers were downloaded and no customer workloads or live databases were changed. Private credentials were tested through mock transports only; real private-repository access and worker-node pull behavior remain unverified.

## Local release storage (not deployed)

Schema 43 adds a distinct `container` project kind and immutable, project-scoped release records. The parent-table migration preserves existing project identities and checks all foreign keys before committing. Every database-consuming binary must be upgraded together; do not run a new binary against the live schema-42 database independently.

Container project creation is disabled by default and has no public configuration switch yet. When enabled internally, containers share the existing Node entitlement and dynamic-application capacity pool, count against the total project allowance, and reject ZIP uploads. This is an implementation guard, not a new advertised plan.

Release preparation authorizes owners/developers before registry resolution and again before saving. Retries retain the original digest even if a tag moves; reusing a request key with different settings is rejected. Records include the source, immutable ARM64 pin, port, health path, actor and revision. Each project may retain up to 50 releases. Ports must be 1024 through 65535 for the non-root profile. Health paths must be local paths without query strings or fragments. No registry credentials or environment secrets are saved in release records.

Schema 44 adds durable deployment requests, cancellation before dispatch, monotonic revisions for restoring retained releases, and a single pending deployment per project. Queued work blocks project deletion. Worker dispatch rechecks the initiating user's current membership, account and hosting entitlement, then persists the complete request and previous successful release before calling the runtime. An uncertain submission remains running for reconciliation and is never automatically resubmitted. A successful submit is only acceptance, not publication.

The fleet now has a structurally serialized container configuration with the selected port and health path, preserving the stateless two-replica ARM64 hosting profile and resource limits. The live worker still does not consume container jobs.

These records do not publish an image. Fleet dispatch, private credential lifecycle, environment settings, customer controls and runtime qualification remain outstanding.

## Required integration before exposing Deploy

1. Add a distinct container project type with a migration that preserves the many foreign keys referencing `projects`. Do not label arbitrary images as Node source uploads. Include capacity/entitlement checks and project deletion.
2. Persist owner/developer-authorized, project-scoped image candidates and immutable release revisions. Keep source tag, resolved digest, registry identity, target platform, settings and actor evidence. Check current permissions again when the worker claims the operation.
3. Add port, health path and environment controls. Render configuration structurally rather than interpolating arbitrary YAML. Enforce the existing stateless hosting profile, resource limits, non-root runtime and network policy. Do not silently enable privileged containers, host paths, custom entrypoint execution on the host or arbitrary egress.
4. Add encrypted project-scoped registry credentials or an operator-controlled credential reference. The current deployer has no image pull secret field or Kubernetes pull-secret reconciliation. Keep pull credentials separate from application environment secrets and out of status/logs. Rotation and deletion must preserve secrets still needed by an active or rollback release.
5. Extend fleet assignments/provisioning and worker dispatch with a registry branch. Bypass archive image builds. The existing renderer and `healthy` helper hardcode 8080, `/` and two replicas; make registry handling settings-aware without changing existing static/Node defaults.
6. Connect credential references through the deployer to owned Kubernetes pull secrets, or implement an equally scoped verified import into the private registry. Preserve digest integrity, bound transferred/expanded bytes and avoid sharing one customer's credential with another. Confirm registry reachability from the Pi workers.
7. Persist dispatch intent and previous release before deployment. Reconcile uncertain results through the deployer, require actual readiness and route health, and retain the previous digest/settings/credential reference for rollback. API acceptance alone is not successful publication.
8. Add the customer create/check/publish/progress/rollback path and qualify one public and one private image end to end on the fleet. Only then advertise registry deployment in the landing page and onboarding.

GitHub deploy-on-push and isolated Dockerfile builds remain separate subsequent work in the original launch plan.
