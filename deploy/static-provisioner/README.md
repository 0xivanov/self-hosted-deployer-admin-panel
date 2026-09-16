# Bounded automatic static preview

The VPS timer assigns up to five new static projects automatically. Existing
publication mappings, including the original pilot, are left untouched. This is
an operator-enabled private preview, not an unlimited paid hosting scheduler.
Create a static project, upload a ZIP, refresh its release status after setup,
and publish. Setup usually needs a minute for its public HTTPS certificate.

Each assignment has a persisted slot (ports 8900 through 8909), dedicated Linux
user, private content directory, project token, runtime and worker units, and a
`site-<24 hex chars>.159-195-146-26.sslip.io` address outside the portal domain.
Customers do not control commands, port numbers, hostnames or file paths. No
uploaded code is executed. Only exact static project IDs from the portal DB are
used. Existing portal publication/worker authorization and hosting gates apply.

The controller runs as root because it creates users and system services. Keep
its script root-owned. It reads the portal SQLite database in read-only mode,
serializes runs with flock, reserves slots before side effects, and retries
incomplete assignments with the same token and slot. It publishes the mapping
atomically only after the public certificate is ready. Portal requests reload
that private file without a restart; invalid reloads disable publication.

Install the Python script as `/usr/local/libexec/launchstead-static-provision.py`
and both unit files under `/etc/systemd/system`, then enable
`static-provisioner.timer`. This deployment expects the original pilot binaries,
portal settings, VPS VPN address, k3s/Traefik, cert-manager issuer, openssl,
Python 3 and Linux account tools. It is specific to this VPS. Preserve the
registry `/var/lib/launchstead-provisioner/assignments.json`: never reassign
slots while a runtime still exists. Prefix collisions fail closed.

Backups include `/var/lib/launchstead-provisioner`, `/etc/launchstead-sites`,
`/var/lib/launchstead-sites`, the controller script and units. Runtime/worker
units are added to the existing recovery configuration before an assignment is
made available. Portal settings and binaries are covered by the pilot backup.
For a consistent manual snapshot, stop the timer, controller and all publication
workers, run the recovery backup, then restart workers and timer. Content can
continue serving. Full restore and simultaneous scheduled backup/publication
qualification remain open.

Disable the timer to stop new assignments; existing websites remain online.
Stopping a site's worker stops publications, not serving. No automatic deletion,
slot reclamation, runtime repair after assignment, disk/bandwidth metering or
origin certificate renewal is implemented. Origin certificates need rotation
before September 2027. Public certificates renew through cert-manager. Capacity
exhaustion is logged; unassigned projects remain in Hosting setup pending.
Production enrollment, resource accounting, teardown and Node.js hosting remain
separate work.
