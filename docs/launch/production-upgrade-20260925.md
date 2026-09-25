# Coordinated production upgrade, September 25, 2026

The first rollout installed core `1a00d2e` and admin-panel `81407a8`. Core migrated from
schema 6 to 12; the portal migrated from schema 42 to 46. The worker-agent
binaries on the two Pis were not replaced in this rollout. The changes to the
candidate deployment protocol run in the VPS control plane against Kubernetes.

## Installed artifacts

Built for Linux AMD64 with CGO disabled and `go build -trimpath`:

- `/usr/local/bin/deployer-server`
- `/opt/deployer-admin-panel/deployer`
- `/opt/launchstead-portal/customer-portal`
- `/opt/launchstead-portal/billing-worker`
- `/opt/launchstead-portal/node-build-worker`
- `/opt/launchstead-portal/node-deployment-worker`
- `/opt/launchstead-portal/publication-worker`
- `/opt/launchstead-portal/billing-plan`
- `/opt/launchstead-fleet/fleet-worker`
- `/opt/launchstead-fleet/fleet-logs`

Updated the provisioning, project-deletion and domain-reconciliation Python
controllers in `/opt/launchstead-fleet`. Binary and controller manifests were
verified before installation. The log service must be upgraded with the fleet
worker because both strictly decode the shared worker configuration.

No feature flags, payment configuration, account allowlist or project capacity
were changed. Docker/candidate operations remain disabled. The upgrade makes
the infrastructure compatible with the new workflow; it is not evidence of a
customer completing a Docker publication through the portal.

## Backup and maintenance sequence

Rollback material is root-private on the VPS at:
`/var/backups/launchstead-coordinated-20260925`.

It contains the original binaries/controllers under `files/`, the prior service
and private configuration archive, consistent `core.sqlite` and `portal.sqlite`
SQLite backups, fleet operation state, the list of previously active units and
an installation completion record. These files contain secrets/customer data;
keep them private. This snapshot has not been separately exported offsite.

The portal and operator panel were stopped to prevent new management requests.
The provisioner, deletion and domain timers and services were stopped, along
with fleet, log, billing and Node workers. Database consumers were additionally
discovered from service command paths, covering differently named legacy/static
publication workers. The core server was stopped last. No queued/running portal
jobs remained. All writers were confirmed inactive before SQLite backup.

After replacing all ten binaries and three controllers, the core started first,
then the portal migrated its database. Integrity, foreign keys and existing
record counts were checked before restoring workers and timers. Application
Pods were left running throughout the management-service maintenance.

## Observed result

The 08:42:27 UTC inventory reported no errors:

- All three nodes Ready; all eight application Deployments at expected replicas.
- Core: 11 apps, 57 deployments, 7 routes, no tracked pending requests.
- Portal: 1 user, 2 sessions, 6 projects, 8 uploads, 3 publications, 4 Node releases
  and 2 project-domain records, all preserved.
- Both databases passed integrity and foreign-key checks; no unfinished portal jobs.
- Previously active management services, workers and controller timers restarted.
- Public portal and `testdomain.0xivanov.dev` returned trusted HTTPS 200.
- The operator panel returned its expected anonymous 401; authenticated CLI app
  listing succeeded against the upgraded server.

The revised inventory also records the log-service binary and discovers portal
consumers by command path rather than relying only on a fixed unit-name list.

## Rollback boundary

Old binaries do not support the migrated schemas. Do not downgrade just an
executable or start an old worker against the new database.

If rollback becomes necessary, first stop ingress to management actions and the
same complete set of writers/timers. Take another consistent snapshot of the
current databases and operation state. Check for any deployment, customer data
or provider activity since this upgrade. Reconcile that activity before deciding
whether restoring the pre-upgrade snapshot is acceptable; a database restore
would otherwise lose newer changes and would not undo Kubernetes/provider work.

For a coordinated snapshot restore, recover the matching prior binaries,
configuration, fleet state and both databases as a set. With all writers stopped,
remove only the restored databases' stale WAL/SHM sidecars and restore the
original service-account ownership and private modes. Start the old core and
portal first, check integrity and readiness, then restore only the workers/timers
recorded in `active-units.json`. Verify public routes and application availability.
Do not extract the full configuration archive over unrelated newer host changes;
restore only the reviewed affected paths.

## Remaining activation work

A private GHCR fixture exists, but actual worker pull/credential rotation is not
yet verified. A separate read-only package token was requested from the owner.
After that check, configure the shared portal/fleet credential encryption key,
container mapping and candidate feature settings, then verify publication through
the portal. Keep existing accounts, projects and application workloads intact.


## Portal follow-up: admin 5e19bcf, schema 53

The current VPS runtime is core `1a00d2e` / schema 12 and admin `5e19bcf` /
portal schema 53. All eight portal database consumer binaries listed above were
replaced together; the core binary, CLI, Pi agents and application workloads
were not replaced. The core service was briefly stopped and restarted as part
of the coordinated backup procedure.

Root-private rollback material is `/var/backups/launchstead-portal-5e19bcf`.
It contains prior binaries, private configuration, consistent backups of both
databases, fleet state, prior active-unit and installation manifests, and a
completion marker. This snapshot has not been separately exported offsite.
The same coordinated rollback boundary above applies; never run old portal
binaries against schema 53 or restore the snapshot without accounting for
subsequent writes and provider activity.

The 11:56:52 UTC inventory and public checks confirmed:

- All eight installed portal binary hashes match the release artifacts.
- Core schema 12 and portal schema 53 pass integrity and foreign-key checks.
- All three nodes Ready and all eight application Deployments at expected replicas.
- Six projects, eight uploads, three publication pointers, four Node releases,
  two domain records, one user, two sessions and zero container releases preserved.
- Core has 11 apps, 60 deployments and seven routes.
- No queued/running publication, Node build/deployment or container deployment jobs.
- Portal and `testdomain.0xivanov.dev` return HTTPS 200; served portal JavaScript
  matches the release.

Client access history is now available to website owners. GitHub connection and
automatic deployment flags remain false, as does container hosting. No signup,
billing, capacity or feature settings changed. GitHub App registration still
requires owner account verification before credentials can be configured. These
checks do not establish a completed live GitHub or Docker publication.

The inventory now includes GitHub imports, active pipelines, unclaimed/running
push processing and successful imports awaiting pipeline creation. It does not
misinterpret the immutable event's `pending` label as ongoing work. A focused
fixture covered those boundaries. The read-only production run at 12:02 UTC
reported no inventory errors and zero unfinished jobs in all these categories.
Future maintenance must stop GitHub intake and its worker along with the other
writers before relying on the drain observation or taking consistent backups.
