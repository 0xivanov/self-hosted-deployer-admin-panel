# Private fleet hosting

Customer websites use the original self-hosted deployer. The portal owns users,
workspace permissions, subscriptions, source uploads, build jobs and release
history. The trusted fleet worker turns retained releases into immutable OCI
images and submits generated hosting specifications to the core deployer.

Public traffic flows through the VPS ingress to two replicas, one on each ARM64
Pi worker. Existing applications without the hosting profile keep their original
configuration. The Mac and the former Linux lab are no longer in the hosting or
build path.

## Components

- VPS: customer portal and SQLite database, fleet publication/deployment worker,
  project enrollment timer, per-project Node build workers, and private registry.
- Pi home: Node build controller on VPN HTTPS `10.8.0.3:8951`.
- Both Pis: website workloads scheduled by the existing k3s/deployer cluster.
- Registry: `10.8.0.1:5000`, VPN-bound TLS, authenticated pulls and pushes.
- Backblaze: existing encrypted recovery backup, extended with fleet state,
  registry data and a restricted SSH capture of builder configuration/state.

`fleet-content` serves static ZIPs without extraction. For Node releases it
starts the validated npm start script on loopback port 3000 and exposes a content
proxy on port 8080. Lifecycle pre/post scripts are disabled. Uploaded Dockerfiles
are never executed. Image packaging copies validated files into operator-owned
base images without running customer commands on the VPS.

Node builds use a pinned ARM64 Node 24 toolchain, verified npm tarballs and
network-disabled Docker containers. Each has a non-root user, dropped
capabilities, read-only root filesystem, bounded temporary storage, a 256 MiB
output filesystem, one CPU, 512 MiB memory and 128 processes. The controller stops
execution before validating/exporting artifacts. This is shared-kernel container
isolation, not VM isolation. It is intended for the invited private launch, not
unrestricted hostile public workloads.

Hosting runs stateless replicas with explicit resource limits and a default
network policy allowing platform traffic and DNS. General outbound application
connections are not enabled. Applications requiring external APIs, databases
or durable local writes need a separately configured hosting profile.

## Enrollment and access

`fleet-provisioner.timer` runs once a minute and assigns up to five projects in
total. Static projects receive publication mappings. Node projects receive a
stable runtime identity, pinned build assignment and a persistent build worker.
The builder's authenticated project router restores and caps five controllers.
New domains use `site-<first 24 project-ID characters>.159-195-146-26.sslip.io`.
Existing custom assignments are retained. This is automatic DNS resolution, not
domain registration or resale.

The portal remains authoritative for workspace membership and hosting entitlement.
Mappings are operator-owned JSON files, never browser-selected runtime URLs.
Node and publication mappings reload per request. Registration checks the private
`/etc/launchstead-portal/signup-allowlist.json` email array on every attempt and
fails closed if it is unavailable or invalid. The launch allowlist currently
contains only the owner's supplied email. Existing users and sessions are not
removed by changing this registration gate.

Stripe remains in test mode. Merchant sales and domain resale are not enabled
in this live portal configuration.

## Installed configuration

- `/etc/launchstead-fleet/worker.json`: database, core CLI/config, image wrapper,
  state directory, and project assignments. Owned by `launchstead-portal`, 0600.
- `/etc/launchstead-fleet/build-template.json`: private shared builder assignment
  used by the root enrollment controller. No browser receives its token.
- `/etc/launchstead-portal/node-builds/<project>.json`: per-project build workers.
- `/etc/launchstead-builder/config.json`: builder configuration; `Project: "*"`
  selects authenticated routing with per-project execution/dependency roots.
- `/etc/launchstead-registry`: root-owned registry credentials, TLS, pinned Node
  runtime image, restricted backup SSH key and pinned Pi host key.
- `/var/lib/launchstead-fleet`: durable publication fences and Node operation
  records. Preserve these with the portal database during recovery.
- `/var/lib/launchstead-image-builder`: root-only temporary image packaging.

The fleet worker runs as `launchstead-portal` to preserve the database's required
0600 ownership. Its only sudo operation is the fixed, root-owned image packaging
binary. That binary reads confined staging archives and uses a separate root-only
build context. The portal and fleet worker are a shared trusted control-plane
account; customer containers receive neither account credentials nor Docker access.

The Node runtime base currently uses:

- Node archive SHA256: `5f4ddab610c1ab2016b3c227cebdbf6d9495161487e4739c7b90090595f465f7`
- Runtime image: `10.8.0.1:5000/runtime@sha256:9807d2cbc78b4f1c30ff6117a2211ec6fbf3b40444c83b28931aa10b7db2c750`

Build `Dockerfile.runtime` on ARM64 with the verified extracted archive in
`toolchain/` and a cross-compiled `node-build-guest` helper. This preparation is
operator work, separate from untrusted customer builds. Configure k3s registry
trust on every worker and restart agents one at a time before private pulls.

## Retry, recovery and rollback

Publication revisions and Node operation identities are durable. Unknown core
deployment outcomes are observed, never blindly resubmitted. A Node preparation
failure that occurred before core dispatch can fail conclusively while preserving
the prior route. Once dispatched, an unresolved core failure remains pending for
operator reconciliation. The portal only records success for the exact desired
image and a healthy complete two-replica rollout. Core commit e51bab6 adds the
required updated/ready/observed-generation health check.

Customer rollback selects a retained release and creates a new deployment revision.
It does not move a live container directory backward. The fleet worker packages
the retained bytes and deploys the immutable image through the same core API.

Before restoring service state, stop its owning worker. Restore the portal DB,
fleet fences, registry and builder tombstones together. Do not erase an uncertain
operation record or rerun an execution ID. The backup pre-step captures the Pi
through a forced-command SSH key that can only export the builder recovery tar.
A failed capture fails the offsite backup rather than silently claiming fresh
builder coverage.

The former ingress definitions are retained at
`/var/lib/launchstead-fleet/previous-ingresses.json`; former runtime files and
service definitions remain for operator rollback. Their units/timers and Mac
launch agents are disabled. Re-enabling them requires deliberately handing back
both public ingress and publication-worker ownership. Never run old and fleet
publication workers against the same projects simultaneously.

## Operational boundaries

The current VPS remains a single control-plane/ingress/registry failure domain.
Two Pi replicas protect against one worker failing, not a VPS outage. Pi home is
the single builder; running websites survive builder downtime but new builds wait.
The current private launch cap is five assigned projects. Increasing this cap or
opening public registration requires a deliberate capacity and isolation review.

## Customer-owned domains

Each project's **Connect domain** control connects an existing domain without
transferring its registration. The customer adds an A record pointing to
`159.195.146.26` and a project-specific TXT ownership proof under
`_launchstead.<hostname>`, then selects **Verify DNS**. DNS must be unproxied
(DNS only in Cloudflare) with no AAAA record for this first version. Apex and
`www` are separate hostnames and should be connected separately. Wildcards and
Unicode hostnames are not supported; use an ASCII/punycode hostname.

The portal authorizes workspace access and verifies DNS before queuing the
hostname. The domain reconciler adds a separate, labeled ingress to the existing
core-managed service. The original project URL and application deployment stay
in place. HTTPS is issued by the existing cert-manager issuer. A domain is shown
as active only when the certificate is ready and the project has a service
endpoint. Publish the project before expecting its custom hostname to activate.

Removal is queued: the worker removes only its own ingress, certificate and TLS
secret before freeing the hostname in the database. Keep the ownership TXT
record while the domain is connected. Domain registration and renewal remain
with the customer's existing registrar. This does not enable domain resale.

The additive `project_domains` table is schema version 39. Upgrade every binary
that opens the portal database together, including fleet, billing and Node build
workers, since older binaries reject newer schema versions. Take a consistent
SQLite backup before migration. The root-owned domain reconciler and its systemd
timer live alongside the fleet provisioner; database operations retain the
`launchstead-portal` account's private file ownership.

## Deleting a project

Workspace owners can delete a project from its **Danger zone** by typing the
exact project name. Developers and viewers cannot delete projects. In-flight
builds and deployments must finish first. A deletion request fences new project
writes and marks custom domains for removal in the same transaction. The card
shows cleanup progress until the background worker finishes; other cards retain
their open panels and inputs.

`project-deletion.timer` runs the root-owned `delete-projects.py` every 15 seconds.
It shares the enrollment and domain reconciliation locks, removes owned domain
routes, retires the project's builder controller, calls the original deployer's
`delete --yes` command, removes private assignments and retained portal data,
and finally deletes the project row. Failed cleanup keeps the row and retries.
The original deployer checks Kubernetes resource ownership and removes the
application's runtime and routing records. Domain registrations and billing
subscriptions are unchanged. The slot is freed after cleanup completes.

The builder persists a deletion tombstone to reject stale execution requests,
while removing its per-project source/dependency files and freeing controller
capacity. Operational backups, deployer audit history, and cached OCI image
layers follow their existing retention lifecycle; project deletion is not an
immediate secure erase of backups or shared caches.

Schema version 40 adds deletion state to projects. Upgrade all portal database
readers together, plus the fleet provisioner, deletion worker, deployer CLI, and
Pi builder binary. The fleet worker supports zero assigned projects so deleting
the last project does not break enrollment of the next one.
