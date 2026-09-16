# Static pilot deployment

This is a one-project proof of concept for the existing static project
`32d444bf366d0e39885b3bfc51519a68335a82c68d8a7ad99ac5815d402316e8`.
It serves the project at
`https://testing-two.159-195-146-26.sslip.io` from the existing VPS at
`159.195.146.26`. It is a manually assigned deployment. It does not provide
automatic provisioning, project selection, or fleet reconciliation.

The static runtime runs as `launchstead-static` with binary
`/opt/launchstead-static/static-runtime`, private configuration
`/etc/launchstead-static/config.json`, and site state under
`/var/lib/launchstead-static/site`. Its content listener is HTTPS on
`10.8.0.1:8793`. Its authenticated management listener is HTTPS on
`127.0.0.1:8792` and is not reachable through the public ingress.

The publication worker continues to run as `launchstead-portal`, using
`/opt/launchstead-portal/publication-worker`, the portal database at
`/var/lib/launchstead-portal/portal.sqlite`, and assignment file
`/etc/launchstead-portal/static-assignment.json`. The assignment must contain
the exact project ID, the runtime management endpoint, and the matching
64-character token. The optional `ca_file` must contain the runtime's public
management certificate or its issuing CA. Keep the assignment private and
mode `0600`.

## Install and start

Build the Linux binaries from this repository and copy them to the paths above.
The parent deployment procedure supplies the runtime config, TLS certificates,
assignment, and token. Create the dedicated user and directories first:

```sh
sudo install -d -o root -g root -m 0755 /opt/launchstead-static
sudo install -d -o launchstead-static -g launchstead-static -m 0700 \
  /etc/launchstead-static /var/lib/launchstead-static/site
sudo install -d -o launchstead-portal -g launchstead-portal -m 0700 \
  /etc/launchstead-portal /var/lib/launchstead-portal
```

Install and verify the units:

```sh
sudo systemd-analyze verify deploy/static-pilot/static-pilot.service \
  deploy/static-pilot/static-publication-worker.service
sudo install -m 0644 deploy/static-pilot/static-pilot.service \
  /etc/systemd/system/static-pilot.service
sudo install -m 0644 deploy/static-pilot/static-publication-worker.service \
  /etc/systemd/system/static-publication-worker.service
sudo systemctl daemon-reload
sudo systemctl enable --now static-pilot static-publication-worker
```

The units use `NoNewPrivileges`, an empty capability bounding set, strict
system protection, private temporary storage, and conservative memory and
process limits. The runtime can write only its site root. The worker can write
only the portal state directory, which is required for SQLite transactions and
its journal files.

## Ingress and origin trust

The manifest in `deploy/static-pilot/ingress.yaml` creates the
separate `launchstead-sites` namespace, an HTTPS service and EndpointSlice for
`10.8.0.1:8793`, a Traefik `ServersTransport`, and the `testing-2` ingress.
The transport verifies the origin certificate with Secret
`testing-2-origin-ca` and requires the certificate identity
`testing-two.159-195-146-26.sslip.io`.

Create the namespace and origin trust Secret from the public origin
certificate. Never place the origin private key in Kubernetes:

```sh
sudo k3s kubectl create namespace launchstead-sites --dry-run=client -o yaml | \
  sudo k3s kubectl apply -f -
sudo k3s kubectl create secret generic testing-2-origin-ca \
  -n launchstead-sites \
  --from-file=ca.crt=/etc/launchstead-static/tls.crt \
  --dry-run=client -o yaml | sudo k3s kubectl apply -f -
sudo k3s kubectl apply -f deploy/static-pilot/ingress.yaml
```

If the origin certificate is issued by a CA, use that CA certificate as
`ca.crt`. Confirm it covers the exact server name before applying the route.
The public certificate is issued by the existing `deployer-letsencrypt`
ClusterIssuer. DNS for the sslip.io host resolves to `159.195.146.26`.

## Operating boundaries

This pilot serves immutable static files only. It has no server-side code
execution, shell, runtime package installation, portal database access, or
fleet credentials. The content hostname is separate from the customer portal's
registrable domain. Management is loopback-only and publication occurs through
the assigned worker and its project-bound token.

Back up `/var/lib/launchstead-static/site`,
`/var/lib/launchstead-portal/portal.sqlite`, the two private configuration
files, the runtime certificates and the installed binaries as one consistent
deployment record. Preserve the active release pointer and release ZIPs so an
older known-good revision can be restored. Treat a publication transport error
as uncertain: retain the job and reconcile or retry the same revision before
attempting another release.

To roll back, first stop the worker and runtime, preserve their state and logs,
then restore the previous runtime binary and site backup. Start the runtime,
verify the previous release through the content URL, and restart the worker
only after its assignment and database are consistent. Remove only the
`testing-2` ingress resources and the `launchstead-sites` namespace if the
pilot is being retired. Do not remove the existing `launchstead-portal`
namespace, portal service, portal database, or unrelated operator services.

## Verified pilot, 2026-09-16

In Brave, the owner uploaded and published the demo ZIP in workspace `testing`,
project `testing 2`, then published a changed ZIP and restored the first upload.
Revisions 1, 2 and 3 succeeded. Public HTTPS content matched each release, and
revision 3 retained version 1 after restarting the runtime. An unauthenticated
management request returned 403; public POST /publish returned 405. Existing
portal and operator services remained active.

The origin certificate expires in September 2027 and needs operator rotation:
replace the runtime certificate/key, update the public CA Secret and the worker's
CA file, and restart the runtime. Public TLS is managed by cert-manager.

The recovery backup includes the static configuration, binaries, state and both
units, together with existing portal sources. For the initial backup the worker
was stopped briefly and restarted afterward; serving remained available. Future
concurrent publication/backup consistency and full offsite restoration still
need qualification. Do not prune release ZIPs independently of portal history.

For a normal website rollback, use **Restore this upload** in the portal. This
creates a new revision from the earlier validated ZIP. The emergency operator
rollback above is for a failed runtime/deployment, not everyday content changes.
New projects still require an operator assignment, runtime and HTTPS route.
