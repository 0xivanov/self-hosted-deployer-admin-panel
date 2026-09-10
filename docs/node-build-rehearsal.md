# ARM64 Linux Node build rehearsal

On 2026-09-10 the checked-in synthetic Node fixture passed a build/start/stop
rehearsal in a new disposable Lima VM, `deployer-node-build-lab`. The existing
control-plane, restore and worker labs remained stopped; no VPS or Pi was changed.

## Environment and scope

- Lima 2.2.0, Apple VZ, ARM64 Ubuntu 24.04, 2 CPUs, 2 GiB RAM, 10 GiB sparse disk.
- Image and SHA-256 are pinned in `deploy/qualification/node-build-vm.yaml`.
- No host directory mounts, imported public SSH keys, SSH-agent forwarding or
  application/UDP port forwarding. Lima's localhost SSH management remains enabled.
- Node v24.20.0 Linux ARM64 archive, SHA-256
  `5f4ddab610c1ab2016b3c227cebdbf6d9495161487e4739c7b90090595f465f7`, verified
  against the official HTTPS distribution checksums before unpacking in the guest:
  https://nodejs.org/dist/v24.20.0/SHASUMS256.txt
- Bundled npm 11.19.0. The fixture ran as guest UID 501, not root.
- The guest has ordinary network access. This is not a qualified untrusted-code
  sandbox and must not receive customer uploads or secrets.

## Exercised behavior

The real Linux ARM64 `deploy/qualification/node-preflight` binary called the
production Node plan and source extraction functions against the uploaded ZIP
and expected digest. The guest-only Python harness executed the resulting steps
against the checked-in `deploy/qualification/node-app` fixture, using a clean
explicit environment and job-local home/cache/config files.

The fixture uses only built-in Node modules, without external dependencies. The
rehearsal established:

1. npm ci runs the synthetic prepare hook and creates its marker.
2. npm run build creates the server output under dist.
3. Offline pruning with hooks disabled completes.
4. npm start with hooks disabled starts the intended script without its prestart hook.
5. An HTTP request returns the expected health payload and production environment.
   The fixture honors PORT, tested on an ephemeral guest loopback port.
6. Process-group shutdown stops the application listener, not just the npm parent.
7. A deliberately broken build returns failure.

The first run failed because npm rejects loading both user and global configuration
from the same `/dev/null` path. Separate empty files fixed this; the clean environment
in the checked-in harness reflects that requirement. The corrected run passed all
checks with Node v24.20.0 and npm 11.19.0.

## Reproduction materials

- VM definition: `deploy/qualification/node-build-vm.yaml`
- Trusted fixture source: `deploy/qualification/node-app/`
- Source/plan helper: `deploy/qualification/node-preflight/`
- Guest harness: `deploy/qualification/node-rehearsal.py`

Build the helper for Linux ARM64 with CGO disabled. ZIP the fixture's contents at
the archive root, compute SHA-256, and copy those files and the harness explicitly
into the guest using Lima copy. Download the exact Node archive in the guest and
verify the checksum above. Run the harness with four arguments: source ZIP path,
expected ZIP SHA-256, helper binary path and extracted Node bin directory path.
It creates and cleans private temporary build directories. It must only run this
trusted fixture; it is not a production build executor.

The rehearsal VM was stopped afterward to release RAM. Its disk and pinned
Node toolchain remain available for further lab work. Temporary Mac inputs were
removed. No customer service is exposed by this VM.

## Still unproven

This does not establish private-network/metadata isolation, package gateway rules,
native or third-party dependency compatibility, AMD64, resource/PID/disk/output
limits, runtime secrets, VM dispatch idempotency/retirement, durable artifacts,
publication readiness/rollback, public HTTPS or a complete Node hosting service.
The harness's subprocess timeouts are not a production resource isolation boundary.
Database worker claims/recovery were tested separately and were not wired to this
VM rehearsal. Those integrations remain necessary before an untrusted pilot.

## Service restriction probes, 2026-09-10

The same disposable VM ran trusted Python negative probes in transient systemd
services. `node-restriction-run.py` supplies the explicit restrictions and invokes
`node-restriction-probe.py`, installed read-only under `/opt/node-restriction-lab/`.
The runner uses sudo only to create/inspect/stop services; probes run as the normal
unprivileged guest user. These probes are separate from the earlier Node app test.

Verified from inside the service's actual cgroup:

- `memory.max=134217728` and `memory.swap.max=0`: memory pressure ended with
  `Result=oom-kill`, within the 128 MiB limit.
- `pids.max=32`: the probe created 31 sleeping children before EAGAIN stopped it.
- `cpu.max=100000 100000`: two busy children produced 20 throttled periods.
- Runtime limit 10 seconds, stop timeout 2 seconds, control-group termination:
  the timeout probe ended with `Result=timeout`; its child PID no longer existed.

Other checks passed:

- A private network namespace blocked TCP connections to metadata address
  169.254.169.254, the legacy VPN address 10.8.0.1 and public address 1.1.1.1.
  These services intentionally have no package-download or public egress.
- No-new-privileges was set in `/proc/self/status`, sudo elevation failed, and a
  write under `/etc` failed with the system filesystem protected read-only.
- 4 MiB per-file limit produced EFBIG. The private `/work` tmpfs filled at its
  64 MiB cap, and `/tmp` and `/var/tmp` each filled at their 16 MiB caps.
- The runner stopped and reset each transient service. No active probe units
  remained, and the VM was stopped afterward.

Two issues found during verification were corrected in the checked-in harness:
completed transient services can be garbage-collected, making later property reads
show defaults, so resource assertions now read kernel cgroup files from inside the
running probe. Also `PrivateTmp=yes` overrode the intended temporary mounts; the
profile now uses explicit bounded tmpfs mounts without that conflicting property.

The complete final profile is in `deploy/qualification/node-restriction-run.py`.
Additional restrictions include protected home directories, cgroups/kernel settings,
private devices, an empty capability bounding set, and blocked namespace creation.
Those settings are not an exhaustive kernel-escape test. The trusted negative
probes exercise selected controls, not hostile customer packages or Node itself
under this profile. The tiny resource values are test limits, not hosting plan sizes.

Still required: a positive Node build under the final restrictions; a safe package
fetch/gateway stage; log limits; pinned system/runtime images; per-job VM dispatch,
operation retirement and recovery; artifact validation/export; and tenant isolation
qualification. Journaling is not yet an untrusted log-retention solution. These
service restrictions supplement the VM boundary and do not authorize sharing a
legacy host or executing arbitrary customer code on the Mac.
