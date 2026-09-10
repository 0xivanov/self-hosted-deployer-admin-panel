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

## Positive Node build under the restricted profile, 2026-09-10

The synthetic Node fixture now passes under the same service profile as the
negative probes, shared in `deploy/qualification/node_lab_profile.py`. The profile
was not relaxed: 128 MiB memory, zero swap, 32 tasks, one CPU quota, a 10-second
runtime limit, blocked outbound networking, no privilege elevation, protected
system/home paths and bounded writable temporary filesystems remained enabled.

Node v24.20.0/npm 11.19.0 passed install hooks, build output, offline prune, HTTP
readiness, suppressed prestart hooks, process-group shutdown and broken-build
rejection under that profile. The fixture has no third-party dependencies, so this
only demonstrates the offline execution stage. Negative resource/network probes
were rerun successfully against the shared profile afterward.

A reboot cleared the earlier `/tmp` rehearsal inputs. The new Mac setup helper
`deploy/qualification/node-lab-setup.py` deterministically packages the checked-in
fixture, cross-builds the Linux ARM64 preflight helper, explicitly copies inputs to
the named running lab, installs read-only fixture/toolchain inputs under `/opt`,
verifies the pinned Node archive checksum and runs the positive test. It accepts no
customer source path and does not create or start a VM. Run it from the Mac after
starting `deployer-node-build-lab`; then stop the VM after qualification. Its local
temporary inputs are automatically removed.

The deterministic fixture ZIP SHA-256 for this run was
`c5dfe6f88bfbe21215d3afce1d97293144fbd3f61045c73fccca5b6b28975c5c`.
`node-positive-run.py` takes the expected fixture digest explicitly and uses the
shared profile. Inputs under `/opt/node-positive-lab` now survive guest reboots;
temporary copies of the runners under `/tmp` can be recreated by the setup helper.

The previous positive-build-under-restrictions gap is closed for this synthetic
ARM64 fixture. Third-party/native package builds, package acquisition, resource
sizing for actual plans, logs, durable artifacts, VM dispatch/retirement and
customer runtime publication are still unqualified. This does not enable public
Node hosting or qualify arbitrary uploaded code.

The same profile also passed a real dependency installed from a verified offline
cache. See [the dependency rehearsal](node-dependencies.md#offline-npm-cache-rehearsal).
The original dependency-free scenario was rerun and still passed after adding the
optional cache path. The lab service was cleaned up and the VM stopped afterward.

## Portal worker through restricted guest execution

Run `python3 deploy/qualification/node-lab-setup.py --worker` with the named lab
VM already running. This builds the guest-only `node-worker-rehearsal` helper and
uses the pinned dependency fixture. It does not accept arbitrary source paths or
customer archives. The helper refuses other operating systems, architectures and
root execution, checks the exact fixture/toolchain archive digests, and creates a
throwaway portal database in a private guest directory.

The rehearsal registers/verifies a synthetic account, creates a Node project,
saves its upload, requests a build, prepares dependencies and calls
`DispatchNodeBuild`. Its fixture executor reopens the worker bundle and verifies
that its source, plan, toolchain and complete manifest match the root-owned inputs
consumed by the restricted service. The service uses the dispatch execution ID as
its unit identity. Before starting, the guest reserves a private, synced exclusive
attempt record; duplicate IDs are rejected before starting another service.

The helper checks actual install hooks, offline dependency installation, build
output, HTTP readiness, disabled prestart hook, process-group shutdown and rejection
of a broken build through the existing restricted fixture runner. It then tries the
same guest execution ID again and requires an exclusive-record failure. Finally it
reopens the portal database and requires redispatch to fail without a second
executor call. Reports are bounded to 256 KiB in the guest helper. Synthetic portal
data is removed on exit; guest attempt records remain under
`/var/tmp/node-worker-attempts-<uid>` until the disposable lab is reset.

Verified on 2026-09-10 with execution
`5d3903c401facaa5ec090c50fa7bd2c50520402bec60c3b5f51207bfe4f16fda`:
`worker_flow=passed`, `executor_calls=1`, `guest_duplicate_rejected=true`,
`restart_redispatch_rejected=true`. Node v24.20.0 / npm 11.19.0 and all existing
restricted positive-fixture assertions passed. An initial cleanup check was
corrected after observing that systemd had already collected the completed unit;
cleanup now verifies inactive/failed state and no main process before reporting.

This is a connected synthetic qualification, not the production executor adapter.
It reuses a preconfigured lab VM and fixed staged inputs, has no network dispatch
protocol or arbitrary-input transfer, and does not qualify hard deadline behavior,
power-loss recovery, durable retirement against delayed requests, build-log handling
for hostile code or release artifact export/activation. Guest attempt records alone
are not retirement proof. A successful fixture submission leaves the portal build
running; it must not be reported as a successful published customer release.
