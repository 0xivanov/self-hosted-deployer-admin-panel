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
its outcome must first be established through the runtime control plane. Future
activation/reconciliation must preserve references for active and pending releases
and remove obsolete references transactionally, before enabling live deployment.

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

## Current boundary

These methods neither start a runtime nor change a public route. There is no
activation-completion API, runtime transport, public deployment endpoint or active
Node release pointer yet. Runtime revision fencing, health checks, preserving the
previous site on failure, retirement/reconciliation and actual rollback remain
required. Deployment requests must not be displayed as successful live deployments.

Integration tests cover idempotency and conflicting targets, pending serialization,
increasing revisions, archive retention including direct database deletion, queued
cancellation, history across restart/archive deletion, assignment/integrity checks,
lease renewal/expiry, no restart redispatch, tenant/role revocation and migration
from schema 21 with retained release data.
