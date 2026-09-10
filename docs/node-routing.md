# Health-checked Node routing

`internal/noderouter` provides a persistent traffic switch for one project on an
assigned isolated Node runtime. It is a library, not a public control-plane server.
It neither starts processes nor provisions VMs. An operator-owned launcher must
first stage the verified release and keep each configured backend listener bound
to that exact release while it is probed and served.

## Assignment and persistence

Configuration contains the exact project/runtime IDs, content host, health path and
up to 16 named backend listeners. Backends must be canonical
`http://127.0.0.1:<port>` endpoints, with no credentials, path, query, fragment or
duplicate endpoint. Environment proxies are disabled. Configuration is copied and
pinned in a private SQLite database; changing it while reopening that database is
rejected. Database files and their containing directory must be private. The
routing database has its own version and is independent of the portal database.

A candidate carries deployment/operation IDs, project/runtime IDs, a revision,
artifact digest and a configured backend name. These are trusted worker inputs,
never customer-supplied URLs or proof obtained from customer app output. The
launcher remains responsible for source/toolchain/architecture and process identity.

## Activation and failure

`Activate` persists the candidate as the newest revision before issuing a GET to
the configured health path. The previous active route remains unchanged while
probing. Health checks have a three-second timeout, do not follow redirects and
accept only 2xx responses. After the probe, a transaction rechecks that the same
request is still the newest revision before replacing the active route.

A failed candidate leaves the old active route intact and consumes its revision.
An identical failed request is not reprobed; a deliberate retry needs a new
revision. Lower revisions and conflicting same-revision payloads are rejected.
A healthy late response cannot replace a newer activation. A cancelled request may
leave a pending fence, but an identical pending request can safely repeat the
read-only health probe after restart. It does not start any application process.
The active listener cannot be reused with a different artifact digest; a different
release needs a separately staged listener before switching traffic.

Rollback selects the old retained release/backend under a new revision. It uses
the same health gate and atomic route update. The router persists both newest
attempt and active candidate, so a failed newer candidate never replaces the saved
active identity on restart. HTTP requests read the current committed route; in-
flight requests may continue against the previous backend. The launcher must drain
those requests before stopping or reusing that backend.

## Permanent operation retirement fences

Routing schema 2 adds immutable operation retirement records. `FenceRetirement`
validates the exact candidate and initially refuses an operation or backend still
used by the active route. Traffic must move to a replacement first. It can fence
a pending candidate or an operation whose activation has not arrived yet. A pending
candidate becomes failed while the old active route remains unchanged.

`Activate` checks retirement both before probing and before committing the result.
A delayed successful health check or retried activation cannot resurrect a retired
operation, including after reopening the database. Repeated fencing with the same
payload confirms the saved tombstone; a conflicting payload is rejected. This
confirmation is not proof that a backend is currently unused. A newer operation
may later use that backend after the launcher completes retirement and allocation.

Fencing does not cancel existing HTTP requests, stop services or prove the pool's
`RoutingDetached` condition. Request draining and protection against concurrent
backend reuse still need to be connected to the runtime controller. Do not delete
tombstones while old activation requests could arrive.

Schema-1 migration preserves the existing active route and revision. Immediate
write transactions serialize competing controllers; full synchronous commits are
enabled. Older router binaries reject schema 2. Tests cover active-route refusal,
a retirement racing a real health request on another database connection,
pre-activation retirement, persisted retry rejection, replacement serving,
backend reuse under a new operation and migration with an active route.

## Serving and current boundary

`ServeHTTP` accepts only the configured content host and proxies to the committed
backend. Forwarded headers are rewritten by the reverse proxy. Missing routes and
local storage errors return 503; upstream transport errors return a generic 502.
TLS belongs at the listener. The content origin must be separate from the operator
and customer account origins, following the platform architecture.

This is not continuous health monitoring, VM isolation or proof that backend bytes
match the candidate digest. The launcher must preserve that binding, isolate code,
manage resources and keep active listeners alive across service restarts. An
HTTP-ready app can fail after the check. No automatic process restart, drain
management, public management endpoint, deployment-worker transport, portal active
pointer or artifact-reference reconciliation is implemented here yet. Production
hostile-workload, crash/power-loss and pilot qualification remain required.

## Verification

Race-enabled integration tests use real HTTP backends and a TLS frontend. They
cover initial unavailability, healthy switching, failed candidates, serving during
a pending probe, persistent active routing, rollback, stale/late candidates,
conflicting identities, active-listener reuse, cancelled-pending recovery, pinned
configuration, strict host handling, forwarded scheme rewriting, unsafe endpoint
rejection and non-followed redirects. Full integration tests, vet and command
builds pass. No customer code or live VPS/Pi deployment was used in these tests.
