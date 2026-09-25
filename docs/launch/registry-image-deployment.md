# Registry image deployment

Status: the supporting server, CLI, portal, workers and controllers were installed in production on September 25. Core schema is 12 and portal schema is 46. Docker/candidate feature flags remain disabled pending the private-registry check and portal publication verification. Existing ZIP-based static and Node deployments remain healthy. Historical sections below describe implementation milestones; see [the coordinated upgrade record](production-upgrade-20260925.md) for current deployment evidence.

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

Container project creation is disabled by default and requires the operator `--container-hosting` flag. When enabled internally, containers share the existing Node entitlement and dynamic-application capacity pool, count against the total project allowance, and reject ZIP uploads. This is an implementation guard, not a new advertised plan.

Release preparation authorizes owners/developers before registry resolution and again before saving. Retries retain the original digest even if a tag moves; reusing a request key with different settings is rejected. Records include the source, immutable ARM64 pin, port, health path, actor and revision. Each project may retain up to 50 releases. Ports must be 1024 through 65535 for the non-root profile. Health paths must be local paths without query strings or fragments. No registry credentials or environment secrets are saved in release records.

Schema 44 adds durable deployment requests, cancellation before dispatch, monotonic revisions for restoring retained releases, and a single pending deployment per project. Queued work blocks project deletion. Worker dispatch rechecks the initiating user's current membership, account and hosting entitlement, then persists the complete request and previous successful release before calling the runtime. An uncertain submission remains running for reconciliation and is never automatically resubmitted. A successful submit is only acceptance, not publication.

The fleet now has a structurally serialized container configuration with the selected port and health path, preserving the stateless two-replica ARM64 hosting profile and resource limits. Local fleet workers can consume container jobs only with the explicit `enable_container_deployments` switch and an ARM64 runtime assignment. This is not enabled on the live fleet.

Runtime submissions retain the full request, assigned domain and revision fence on disk before contacting the deployer. Replays must match the saved request and never issue another deployment. Reconciliation checks the actual desired image, port, health path, stateless hosting limits, replica readiness and HTTPS route, then applies fresh evidence for the exact job. An expired request that provably never reached dispatch can fail once the previous release is confirmed healthy. A dispatched request with an uncertain or unhealthy outcome stays pending; it is not automatically failed or resubmitted. Automated diagnosis of terminal core rollout failures remains unfinished.

Selecting a retained earlier release uses a new deployment revision and retains the last successful release as its predecessor. This uses the same dispatch and reconciliation path, not an untracked direct image replacement.

This implementation is verified with local fake deployer responses, not a fleet rollout. Private credential integration is implemented locally; credential garbage collection and public/private runtime qualification remain outstanding. Environment settings are now implemented locally as described below. Provisioning/deletion integration is implemented locally but has not been installed on the live fleet. Do not enable the operator switch until those integration requirements are complete.

## Local controller integration (not deployed)

The provisioning controller enrolls container projects only when the fleet config explicitly enables container deployments. Container projects share the total fleet cap and receive a stable runtime ID and ARM64 assignment, without starting a Node build worker. The portal mapping defaults to `/etc/launchstead-portal/container-projects.json`; `CONTAINER_PROJECTS` can override it and the portal must use the same path. Retries repair missing portal/site mappings from the retained fleet assignment without changing its runtime or domain. Conflicting or shared runtime identities fail closed.

Project deletion checks pending container jobs before external cleanup, skips Node builder cleanup for containers, removes only the target assignment, and deletes deployment records before their referenced releases. The controller still supports the live schema-42 database, where container tables do not exist. Existing required tables must be present.

Custom-domain ingress copies the numeric port from the project-owned source ingress, restricted to 1024 through 65535. Static and Node projects retain their existing port 8080 behavior.

Focused local controller checks cover disabled/full-capacity admission, repeated enrollment, partial-write repair, identity conflict, deleting-project exclusion, alternate ports, container record/assignment isolation, pending work and older-schema compatibility. These are mocked controller checks, not live cleanup or rollout evidence.

## Local customer workflow (not deployed)

The portal now has a gated Docker project workflow and authenticated API for checking an image, listing retained releases, publishing or restoring a release, and cancelling queued deployment work. It uses the existing session and CSRF protection. Owner/developer authorization is checked before registry access; runtime destinations come only from validated operator assignments. Image checks can be completed while hosting setup is pending, but publishing requires an assignment. Checks save metadata only and do not run the image.

The server flag `--container-hosting` defaults off. Enabling it requires `--container-projects /absolute/private/container-projects.json`, a private JSON map of project IDs to `{ "runtime_id": "64-character-lowercase-hex-id" }`. Assignments are reloaded per request; missing or invalid files remove availability rather than retaining stale permissions. The runtime ID must match the fleet assignment. This is operator wiring for local integration, not an instruction to enable the unfinished feature on the live service.

Browser verification on a disposable local database covered a real public GHCR metadata check, preserved form values through assignment refresh, queued publication and cancellation. No application image was executed or deployed during this check.

Public access works without a credential key. An optional encrypted credential vault enables project-scoped private Docker Hub/GHCR access through the browser controls described below. The portal and fleet flags are separate and both remain off in production.

## Required integration before exposing Deploy

1. Add a distinct container project type with a migration that preserves the many foreign keys referencing `projects`. Do not label arbitrary images as Node source uploads. Include capacity/entitlement checks and project deletion.
2. Persist owner/developer-authorized, project-scoped image candidates and immutable release revisions. Keep source tag, resolved digest, registry identity, target platform, settings and actor evidence. Check current permissions again when the worker claims the operation.
3. Add port, health path and environment controls. Render configuration structurally rather than interpolating arbitrary YAML. Enforce the existing stateless hosting profile, resource limits, non-root runtime and network policy. Do not silently enable privileged containers, host paths, custom entrypoint execution on the host or arbitrary egress.
4. Add encrypted project-scoped registry credentials or an operator-controlled credential reference. The current live deployer has no image pull secret field or Kubernetes pull-secret reconciliation; local core source now implements these behind an explicit credential revision. Keep pull credentials separate from application environment secrets and out of status/logs. Rotation and deletion must preserve secrets still needed by an active or rollback release.
5. Extend fleet assignments/provisioning and worker dispatch with a registry branch. Bypass archive image builds. The existing renderer and `healthy` helper hardcode 8080, `/` and two replicas; make registry handling settings-aware without changing existing static/Node defaults.
6. Connect credential references through the deployer to owned Kubernetes pull secrets, or implement an equally scoped verified import into the private registry. Preserve digest integrity, bound transferred/expanded bytes and avoid sharing one customer's credential with another. Confirm registry reachability from the Pi workers.
7. Persist dispatch intent and previous release before deployment. Reconcile uncertain results through the deployer, require actual readiness and route health, and retain the previous digest/settings/credential reference for rollback. API acceptance alone is not successful publication.
8. Add the customer create/check/publish/progress/rollback path and qualify one public and one private image end to end on the fleet. Only then advertise registry deployment in the landing page and onboarding.

GitHub deploy-on-push and isolated Dockerfile builds remain separate subsequent work in the original launch plan.

## Known unsubmitted failures

The fleet worker now saves a `not_submitted` outcome when local setup, preflight or a deadline check rejects the request before `DeployApp` is called. Reconciliation can make that attempt retryable immediately, without waiting for its activation deadline. For an update, the previous release must still pass its health/configuration checks before the attempt is marked failed. Restarting the worker cannot replay the rejected request.

A transport error after calling `DeployApp` is still ambiguous. The worker does not turn elapsed time into proof of failure or submit a second deployment. Fully dispatched rollout failures still need a recovery protocol that proves the rejected candidate cannot activate later. This change is local only and does not enable container hosting on the live fleet.

Focused verification covers prompt settlement before deadline, previous-release health, restart replay prevention, omission of upstream error details from the saved operation, and the existing lost-reply behavior.

## Private-image implementation decision

Private pull credentials require changes to the underlying deployer, not only a password field in the portal. Use separate encrypted app-scoped, immutable credential revisions and generated, app-owned Kubernetes image-pull Secrets. Do not put registry credentials into application environment Secrets or allow customers to choose Kubernetes Secret names. Release configuration should retain only an opaque credential revision reference. Rotation must retain revisions referenced by active and restorable releases; deletion must reject references that are still needed. Resolve the same revision during rollback. Write APIs return metadata only and audit records omit credential values.

Before enabling this path, wire the project-to-app authorization boundary, credential creation/rotation, release references, deployer resolution, owned pull-Secret reconciliation, and cleanup together. Qualify a real private ARM64 image and restoration after rotation. Public-image hosting remains gated until the documented runtime and recovery requirements are met.

## Core private-pull support, September 23

Core commit `50ef655` provides an operator-only credential creation/listing service and private-file CLI, encrypted application-scoped immutable revisions, `imagePullCredential` configuration, separate owned Kubernetes pull Secrets, original-revision rollback and final application cleanup. Migration 7 is additive in the deployer database. Existing public-image configurations retain their current path. Focused temporary-database and fake-Kubernetes checks passed; no live server, database, worker or workload was changed.

The portal and fleet integration below now connects these APIs locally. Unambiguous recovery after dispatched failures and actual private ARM64 pulls remain required. Credentials staged before an initial app exists need orphan cleanup; individual revision garbage collection must respect retained releases. See core `docs/private-registry-images.md` for lifecycle and compatibility details.


## Portal private-image integration (local, schema 45)

Owners and developers can save up to 20 immutable registry access revisions per container project, select one while checking an image, and retain its opaque ID with the release. Viewers cannot save access or prepare releases. Credential creation requires the existing session, CSRF protection and current hosting entitlement. Reusing a creation request key with different values is rejected. Passwords are cleared from the form after saving; refresh preserves unsaved form input. Responses and audit/history records expose metadata only.

Schema 45 stores AES-256-GCM ciphertext bound to project, revision and registry. The portal decrypts only for the bounded registry metadata reader. Fleet submission resolves the same project/revision, registers it with the app-scoped deployer API before preflight, and places only the opaque ID in deployment configuration. The CLI bridge uses a private temporary file, removes it after the call and sanitizes errors. Readiness checks require the expected credential revision. Restoring a retained release retains its original credential revision.

### Rollout wiring

- Rebuild and coordinate all seven portal database consumers: customer-portal, billing-worker, node-build-worker, node-deployment-worker, publication-worker, billing-plan and fleet-worker. The live database is still schema 42; do not independently start a schema-46 binary against it.
- Install the matching core server and deployer CLI with migrations 7 and 8 support before enabling private submissions. Back up core and portal databases, binaries and configuration first. Pause writers and timers, migrate with writers stopped, verify integrity and preserved records, then restart the previously active services. An old portal binary cannot read schema 46; do not restore an older database after accepting new writes.
- Supply a randomly generated 32-byte key encoded as 64 hexadecimal characters in an owner-private regular file. Configure the portal with `--container-credential-key-file /absolute/private/key` and the fleet JSON with `container_credential_key_file`. Both require container hosting enabled in their respective configuration. Both services must receive identical key material. If they have different service users, use separately owned private copies, not group-readable permissions.
- Back up the key securely and separately from the database. Replacing it makes existing saved credentials unreadable. Never commit the key, paste it into logs or place it in command arguments.
- Keep container feature flags disabled until controlled public/private ARM64 publication and restoration after credential rotation are verified. Recovery of definitively failed dispatched rollouts still needs completion before advertising the planned Docker product.

Credential revisions cannot be individually removed yet. Old credentials remain available to retained releases; final project cleanup cascades portal records and core application cleanup removes app credentials and owned pull Secrets. Orphan staging and reference-aware revision garbage collection remain follow-up work.

Focused local verification covers encryption and associated-data tamper rejection, migration preservation, role/project isolation, CSRF, request retries, CLI temporary-file cleanup, register-before-preflight ordering, rejection without deployment and credential-specific health checks. A disposable local browser check used synthetic credentials and confirmed immediate selection after saving and cleared login fields. This is not evidence of a real private registry pull or a live deployment.


## Environment settings (local, schema 46)

Owners and developers can now save encrypted, immutable versions of environment settings and select one while preparing a release. Each version contains a label and up to 64 name/value pairs; the API returns only its ID, label and sorted names. Values remain hidden after saving. Refresh preserves unsaved fields, saving updates the selector immediately, and an empty version can clear variables. Saving settings alone does not change the running app; publish a release with the selected version. Restoring a retained release preserves its environment ID.

The existing `--container-credential-key-file` and fleet `container_credential_key_file` now enable both registry credentials and environment settings. Distinct encryption purposes bind environment ciphertext to its project and revision. Schema 46 adds `container_environments` with project-cascade cleanup. There are up to 50 saved versions per project, 128 bytes per variable name, 8192 bytes per value and 32 KiB for all names and values together. Empty values are allowed; NUL bytes and invalid names are rejected.

Before preflight the fleet resolves the exact project/version and stages it through `deployer environment create --values` using a private temporary file. No values enter the fleet operation journal or YAML. The deployer stores encrypted app-scoped bundles in migration 8 and injects them through separate immutable Kubernetes Secrets. Its publish/rollback path resolves the corresponding version, rather than changing mutable settings on a running app. Existing ZIP/Node deployments and legacy deployer secrets retain their prior behavior.

Local checks cover API authorization/CSRF, cross-project reference denial before registry calls, request retries, size limits, ciphertext binding, migration preservation, private-file cleanup and registration before deployment. Browser checks with synthetic data verified refresh preservation, immediate selection, cleared fields and an empty settings version. Core checks additionally cover value injection, original-version rollback, immutable Kubernetes references and app cleanup. This remains source-level and local-runtime evidence, not a live fleet rollout.

## Confirmed failed-apply withdrawal (local)

The container worker now requests structured withdrawal reporting from the matching deployer CLI. A positive receipt is accepted only when its app/deployment identity and requested configuration match the intended release and the complete canonical preflight response. The worker persists the receipt's app/deployment IDs and `withdrawn` stage without upstream error details. Restarting cannot resubmit that attempt.

For an update, settlement still waits for fresh confirmation that the previous configuration, environment/image-access revisions, replicas and HTTPS route are healthy. The latest core deployment must be the exact failed attempt identified in the receipt. This deliberately allows a healthy restored runtime while its latest attempt is recorded as failed. Initial confirmed cleanup can settle without a predecessor.

Core compensation is opt-in. It reports proof only for definitive apply rejections or failures after acknowledged apply, and only after rollback/cleanup and failed-status persistence succeed. A runtime timeout or uncertain server error cannot produce proof. Legacy callers retain their current behavior. This is implemented and verified locally, not deployed.

Lost replies and accepted-but-stuck rollouts remain unresolved. They continue pending rather than risking duplicate deployment. Durable operation identities, withdrawal tombstones and runtime fencing remain the next recovery work before fleet qualification and enabling Docker hosting. See core `docs/deployment-withdrawal.md`.


### Durable response recovery, September 23

Implemented locally: the worker saves a stable deployment request ID and complete preflight configuration before submitting through the tracked CLI. After a lost response it reads the core journal by app and request ID, without replaying the deployment. Applied results require matching app/deployment IDs and fresh runtime, replica and HTTPS readiness. Withdrawn results must match the complete candidate configuration and require previous-release health where applicable. Pending, unknown or mismatched results cannot mark a release live or retryable.

Focused client/worker checks cover lost responses, restart replay prevention, pending/not-found records, changed identity/configuration, fresh readiness and durable withdrawal receipts. Core server/repository checks cover result persistence across database reopen, immutable request identity, predecessor retention and pending mutation blocking. This remains local, not enabled on the live fleet. Stuck-rollout fencing and coordinated schema upgrades remain outstanding.

### Pending recovery review

The current Kubernetes controller mutates resources under stable app names. Its process-local app lock and API resource versions do not establish a durable request fence across server restart or a delayed request. Consequently, elapsed time and a healthy previous image are not sufficient evidence to clear a pending request. Do not add a portal retry or recovery endpoint that merely repeats app-name-based reconciliation.

The next runtime change must establish an enforceable boundary between candidate generations and active routing. A database epoch check before a Kubernetes write alone has a check/write race and is not sufficient. Review a generation-isolated candidate design with conditional activation of the stable route, or an equivalently enforced fencing mechanism. Verification must include a paused old writer resuming after recovery and a server restart; neither may reactivate the abandoned candidate. Only then may recovery restore the predecessor and release the pending reservation.

The pending portal display now includes a continuously updated elapsed wait and full support reference. After five minutes it distinguishes a cancellable queued request from a running request whose outcome is unconfirmed. These updates do not replace forms or require a changed server snapshot. This UI change is local and does not implement runtime recovery.

## Automatic candidate advancement (local, September 24)

The fleet worker now supports `enable_candidate_operations`, default false and requiring container deployments to be enabled. It records candidate mode per operation before submission; enabling the setting does not adopt older saved operations. Portal reconciliation invokes an explicit advance hook, separately from read-only inspection, after rechecking actor membership, verification, account status, project deletion and hosting access.

New container intents allow ten minutes for staging/activation. Advances use that deadline. Expiry or lost authorization starts recovery, not a terminal failure claim. The worker persists `recovering` before the recovery RPC and never returns that operation to advancement after a restart or lost reply. Fresh recorded outcomes, matching configuration/identities and runtime/HTTPS checks still determine success or retryability. A missing or unconfirmed core result remains pending.

Focused worker/client checks cover deadline handling, durable recovery, identity mismatches, disabled mode, legacy records and read-only observation. Portal integration checks cover current authorization and expiry. This is source-only: no production flags or services changed. Before enablement, finish recovery for requests interrupted before runtime checkpoints, candidate-aware cleanup and real-cluster late-write qualification. Upgrade the core server/CLI and all relevant portal workers together.

### Candidate project removal

Core `DeleteApp` now supports bound candidate history and hidden initial
withdrawals. The existing project-removal controller already retries this CLI
operation before purging the portal project. New submissions and preflight are
blocked while the deletion Service marker is present. Cleanup waits for all
recorded candidate releases, the legacy Deployment and all app-owned Pods to
drain; it preserves other projects and the namespace. The Service remains until
database cleanup completes, allowing retries after interrupted replies.

The source implementation and focused checks are complete in core `fe79007`,
with no new database schema. It is not installed in production yet. Candidate
and legacy zero-replica tombstones remain intentionally. Reclaiming running
capacity after successful updates is a separate remaining step, as are
missing-request recovery and real-cluster rollout qualification.

### Reclaiming capacity after updates

After core commits an applied candidate release, explicit advancement now retires
older bound generations and the legacy predecessor, waiting for observed drain.
It preserves the selected candidate, routes and versioned release inputs. A
pending cleanup returns a retryable error while the saved deployment receipt
remains applied. Read-only request lookup does not perform cleanup.

The fleet worker now calls advancement for applied receipts as well as pending
ones. For applied receipts this is cleanup only: the original activation deadline
and revoked activation authorization do not trigger recovery or another deploy.
Cleanup errors prevent portal settlement and are retried on subsequent worker
passes. The deadline still bounds activation for pending requests.

Core `273999b` and the corresponding fleet change are source-only. Focused checks
passed for retained active generations, pending recovery predecessors, expired
deadlines and retry after worker reconstruction. Lost initial submissions without
a request record and real-cluster qualification remain before production enablement.

### Missing submission recovery integration (September 25)

The fleet worker now saves the original YAML alongside each tracked operation.
After the activation deadline, revoked activation authorization, or persisted
recovery intent, a failed request lookup invokes the core withdrawal RPC with
that same YAML and request ID. No error string is treated as proof of absence.
Core atomically records withdrawal if the request is missing, resumes recovery
if it is pending, and preserves an applied race winner. The worker checks the
returned identity and full preflight state, and still requires ordinary fresh
observation before settling the portal job. Applied winners go through earlier
workload cleanup. A lost withdrawal reply is retried after restart without a
second deployment submission.

Recovery intent is saved before dispatch, including if the CLI lacks the new
operation. Older local operation records without saved original YAML remain
pending for operator review; they are not reconstructed or resubmitted. This is
source-only and requires core 68df8a4 (schema 12) plus the updated CLI and fleet
worker. Focused worker/client tests and portal reconciliation integration checks
passed. Actual cluster qualification and the coordinated production rollout
remain outstanding.
