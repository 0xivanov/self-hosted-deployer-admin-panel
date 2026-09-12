# Node deployment requests

Schema 22 adds a durable Node deployment queue. `RequestNodeDeployment` selects a
retained release from the same project and records its immutable artifact digest,
an operator-assigned runtime ID and a monotonically increasing project revision.
Selecting an older retained release creates a new revision through the same path,
so future runtime fencing can distinguish rollback from a stale worker request.
The runtime ID is an opaque 64-character hex assignment, never a customer URL.
Public API wiring must resolve this from approved operator configuration.

Requests require a current verified owner/developer. Reusing a request key with
the same release/runtime returns the existing job; changing either conflicts.
Only one queued/running deployment is allowed per project, enforced by a database
index. At most 1,000 deployment records are retained per project at this stage.
History records keep the release's build ID and digest even after an archive is
removed. Listing is limited to the latest 100 revisions and requires current
owner/developer access.

A separate `node_deployment_releases` table retains the archive for pending work.
Both an application check and a restricting foreign key prevent deletion through
`DeleteNodeRelease` or directly through the database while this reference exists.
Queued cancellation is audited and idempotent, removes only this reference, and
preserves request/history identity. Cancelling a running operation is rejected;
its outcome must first be established through the runtime control plane. Activation reconciliation preserves the active reference and transactionally
removes obsolete or failed references. The active pointer independently references
the retained archive, so database constraints protect it too.

## Worker claims

`ClaimNodeDeployment` is a trusted worker operation. The runtime assignment,
toolchain digest and architecture must match the selected release. The store
rechecks the original submitter's current permission and validates retained archive
bytes before creating a unique operation ID and a hashed one-minute worker lease.
No browser session needs to remain open. Archive bytes and lease secrets are
excluded from JSON serialization of the claim.

`RenewNodeDeploymentLease` requires the exact running operation and unexpired
lease and rechecks current permission. Running operations are never reclaimed,
even after expiry or restart. Permission loss or expiry does not prove a runtime
stopped or that a delayed activation cannot arrive. The worker must stop dependent
work and reconcile; it cannot cancel the job locally or release its archive.

## Runtime reconciliation

Schema 23 adds recorded runtime observations and a persistent active deployment
pointer. `ReconcileNodeDeployment` reads fresh evidence from the assigned trusted
runtime and matches project, runtime, deployment, operation, revision, artifact,
toolchain and architecture. The runtime must report the operation settled, with
no outstanding provisioning or delayed activation able to change its result.
For success, the active route must exactly equal the expected candidate and a
fresh health check must pass. A failed candidate must be stopped, and its reported
remaining active route must exactly match the previously recorded active route.

Completion, active-pointer changes, evidence, audit and archive-reference updates
share a transaction. Failure preserves the old active release. Success releases
only the previous deployment reference; the new active archive stays retained by
both its reference and the active pointer's foreign key. Repeated reconciliation
of a completed job does not query the runtime or apply changes again. Expired
leases do not prevent recording a proven settled result after worker restart.

This operation records facts and does not initiate activation. If the submitter
lost permission after a route was activated, the actual active route is recorded
and the audit action includes `submitter_revoked`. Claim/renewal still deny further
work by that submitter. Stopping or reverting an already-active route requires a
separate authorized runtime operation; falsely reporting it cancelled would hide
what is serving. `ActiveNodeDeployment` is available only to current project
owners/developers. A normal deployment request cannot silently move a project with
an active route to a different runtime assignment.

The runtime adapter must authenticate the assigned service, verify the actual
listener-to-artifact/toolchain binding, take a fresh observation, prove operation
settlement and failed-candidate cleanup, and check active health. Router snapshot
metadata alone is insufficient, and customer app output is never this evidence.
The observation timestamp must use the observing worker's clock for comparison
with request start; a cached remote timestamp is not a fresh observation.

## Current boundary

Queue/recovery methods do not start runtime processes or expose a browser endpoint.
The routing core performs actual health-gated switching, but the production
launcher, authenticated transport, deployment-worker orchestration, draining and
hostile-workload/recovery qualification remain required. This store's completed
records must not be used to bypass those runtime guarantees.

Integration tests cover idempotency and conflicting targets, pending serialization,
increasing revisions, archive retention including direct database deletion, queued
cancellation, history across restart/archive deletion, assignment/integrity checks,
lease renewal/expiry, no restart redispatch, tenant/role revocation and migration
from schema 21 with retained release data.

Reconciliation tests additionally connect the portal store to a real router with
HTTP test backends, independently probe active health, stop the failed test backend,
and verify failure preservation, expiry/restart recovery, idempotency and archive
reference cleanup. Contract tests reject missing/mismatched/stale evidence, verify
post-activation revocation recording and migrate an outstanding schema-22 operation.
These tests do not qualify a production Node launcher or artifact/process identity.

### Durable runtime dispatch

Portal schema 24 adds an immutable deployment dispatch intent. Trusted workers
call `DispatchNodeDeployment` with the current operation and lease. The store
rechecks authorization, retained release linkage and actual archive integrity,
then commits the exact operation, deployment, revision, project/runtime and
artifact/toolchain/architecture pins before one bounded runtime submission.
Archive bytes travel separately; intent JSON contains neither archive nor lease.

A lost reply, process restart or concurrent call cannot resubmit that operation.
Acceptance does not mark it deployed. Actual routing and process facts still
require reconciliation. `ActivateBefore` is a one-minute staging/activation
boundary, not an expiry for an already healthy website. A concrete runtime
receiver must enforce this deadline, durable deduplication, artifact/pin checks
and retirement fences. The provider interface is not yet a runnable production
transport or worker loop.

Migration preserves existing rows and marks legacy running operations as
unreconciled rather than assuming they were never dispatched. These cannot be
submitted by the new method; reconcile them first. Queued operations can acquire
fresh claims normally. This development migration has not touched live customer
data, and older binaries reject the newer schema.

### Customer panel controls

Node project cards expose builds, saved releases, deploy/restore actions, queued
cancellation and deployment history through `/api/node`. Owners and developers
can manage these operations; viewer accounts keep read-only upload listings.
A queued deployment is shown as pending, and only a reconciled active deployment
gets an Open website link.

Enable assigned projects with the customer portal's `--node-projects` private JSON
file. For each project ID, supply `runtime_id` and `build`, for example:

```json
{
  "PROJECT_ID_64_HEX": {
    "runtime_id": "RUNTIME_ID_64_HEX",
    "build": {
      "Settings": {"Architecture": "arm64", "SkipBuild": false},
      "ToolchainSHA256": "PINNED_TOOLCHAIN_64_HEX"
    }
  }
}
```

Use `--publication-sites` for the project's assigned HTTPS website origin.
Configuration is copied and validates exact IDs, architecture and unique runtime
assignment per project. Omit an assignment until its workers are configured.
The UI and API enqueue real work but do not execute customer code in the portal;
the production runtime receiver and worker loop remain under implementation.

### Deployment worker command

Run a separate trusted worker with `node-deployment-worker --database PATH
--assignment PATH`. Its private assignment JSON contains `endpoint` (bare HTTPS
management origin), `project`, `runtime`, `token`, `toolchain_sha256`, `architecture`
and optional `ca_file`. The assignment must be a private regular file no larger
than 16 KiB. Secrets and provider response bodies are not logged.

The worker polls every five seconds. It claims queued work, records its dispatch
intent and submits the archive to the assigned runtime. Running operations are
only reconciled from fresh status, including after worker restart or a lost reply.
Acceptance alone does not mark a website live. Unresolved operations remain pending.

`noderuntimeapi.DeploymentHandler` adds POST `/v1/deployments` alongside the existing
status API on a private TLS listener. It authenticates project/runtime scope,
validates bounded ZIP bytes and request identity/deadline, and delegates durable
acceptance to a trusted runtime provider. The listener must configure read/write
and header timeouts. The provider must implement persistent deduplication and the
installation/start/routing workflow; that concrete receiver remains unfinished.
Do not point workers at a status-only runtime and expect requests to execute.
