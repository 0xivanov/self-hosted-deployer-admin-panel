# Restricted Node runtime services

`internal/nodelaunch.Render` prepares a Linux systemd unit from a trusted runtime
assignment. It does not install, start or observe a service. Identifiers, release
directory names, architecture, UID range and ports are validated before rendering;
uploaded commands and environment values cannot become systemd properties.

The unit runs the pinned Node/npm toolchain with `npm start --ignore-scripts`, a
clean environment, a read-only release, no capabilities, restricted device/kernel
access and a dedicated UID in the reserved 60000–60999 range. Limits are 256 MiB
memory, no swap, 64 tasks, one CPU, 16 MiB per file and bounded temporary mounts
(64 MiB work directory and two 16 MiB temporary directories). Crashes restart after
three seconds, subject to three starts per minute. Stop kills the control group.

The trusted launcher must reserve each UID exclusively, verify its account and
supplementary groups, and keep the reservation until all processes have stopped.
It must also verify artifact/toolchain bytes and actual architecture, seal root-owned
release paths, and provision empty npm configuration files before installation.
These launcher responsibilities are not implemented by the renderer.

## Verified Linux release installation

`InstallRelease` is a Linux/root-only archive installer. It requires a root-owned
destination that is not group/world writable. It validates the expected archive
digest and existing Node artifact rules, extracts under a private staging parent,
then seals regular files to 0444 (0555 for executables) and directories to 0555.
All extracted entries must be root-owned. Validated internal file symlinks remain
links; permission changes never follow them. Customer scripts are not executed.

After syncing the sealed tree, installation publishes a fresh random release name
using Linux `renameat2(RENAME_NOREPLACE)`, then syncs the source and destination
parents. No existing release is replaced. There is no fallback on filesystems
without no-replace rename support. Failed pre-publication staging is removed. An
error after publication returns a nonempty release directory for reconciliation;
callers must retain it and must not start it based on an error return. A crashed
installer can leave private staging or an unreferenced release, so orphan recovery
and durable operation-to-installation binding remain required.

The destination belongs inside the project's dedicated runtime. Read-only Unix
permissions are not cross-tenant filesystem isolation. Toolchain verification,
account provisioning and service lifecycle fencing remain separate steps.

Linux-root tests run with `python3 deploy/qualification/node-install-check.py` in
the already-running disposable VM. On 2026-09-10 they passed ownership/mode and
symlink checks, two independent installations, invalid digest/path/secret rejection,
cancelled request cleanup and unsafe destination rejection. Real subprocesses
running as UID/GID 60000 could read installed files and internal links but could
neither overwrite a file nor create one in the release directory.

## Durable service reservations

`OpenPool` opens a separate private SQLite database pinned to one project's
runtime, architecture, toolchain and 2–16 operator-reserved UID/port pairs. Each
runtime needs its own isolation boundary; pools do not coordinate host-wide UID
allocation across unrelated databases. The caller must reserve the configured
accounts and ports in that boundary before use.

`Reserve` chooses a free pair and persists the complete immutable assignment.
An identical operation returns its current state. Conflicting operation payloads
are rejected, including after retirement. `ClaimStart` changes `reserved` to
`starting` exactly once, before service-manager dispatch. Lost responses and
expired caller processes do not authorize another claim. `Outstanding` enumerates
occupied slots on startup without starting anything.

`BeginRetirement` changes an unfinished operation to `retiring` and closes new
claims. Its slot stays occupied. `ReconcileRetirement` requires a fresh observation
from a trusted adapter, matching the entire assignment, proving all of:

- A durable service-manager fence rejects even previously claimed, delayed starts.
- The UID and service cgroup have no surviving processes.
- The listener is gone.
- Active, pending and draining routing references are detached.

Only then are the evidence and `retired` state committed together and the slot
made reusable. Retirement tombstones remain permanently. Later retries for an old
operation cannot release a newer operation's reused slot. Provider failures,
cancellation, missing evidence, stale timestamps and mismatched identities keep
the slot occupied. The adapter must timestamp freshly collected observations on
the local clock; remote timestamps must not be passed through unverified.

Immediate SQLite write transactions and unique indexes serialize allocation and
start claims across database handles. Full synchronous commits are enabled. Tests
cover concurrent competing handles, process exit without closing the database,
reopening, immutable retries, start authorization, evidence rejection, slot reuse
and permanent retirement history. These tests do not prove disk power-loss
durability or actual Linux fencing. No production retirement adapter exists yet.
Do not replace this database with an older backup while any old service or delayed
request can exist; recovery must first isolate and retire that runtime.

`PrivateTmp=no` preserves the explicit bounded temporary mounts. Dynamic users are
not used because systemd forces private temporary directories for them; see the
[systemd execution contract](https://raw.githubusercontent.com/systemd/systemd/v255/man/systemd.exec.xml).
Node JIT remains permitted. The IP filter permits loopback only. It does not
isolate loopback services from one another, block Unix sockets, force application
bind addresses, or grant access to external APIs. A dedicated runtime boundary and
protected management endpoints are still required. Host support must be verified
with actual traffic, not inferred from accepted unit syntax.

## Disposable VM evidence

On 2026-09-10, `python3 deploy/qualification/node-runtime-setup.py` passed on the
already-running `deployer-node-build-lab` Ubuntu 24.04 ARM64 VM with Node 24.20.0.
The wrapper cross-compiles a trusted renderer helper on the Mac and runs only a
fixed synthetic fixture in the VM. It requires the verified Node archive staged
by the existing Node build rehearsal. It is not a customer deployment entry point.

Successful operation:
`7afe94ee130c4e318870d5316dcbcc6e2ecea8883aaf4d7c8149d57d541c812b`.

The connected installer/service rehearsal also passed on 2026-09-10 with operation
`ba3eaa62f04f48ce90195a262fb5da267c6f81392cbf4f39b5cd67fef9f4d53f`.
It exported the synthetic fixture to ZIP, installed it through `InstallRelease`,
then rendered and started the restricted service using the returned release name.
Artifact SHA256:
`afd16442fcbc8ee926be68c5d69bd84320d844bc89cc2ec6885902736c7f526d`.
All runtime checks, two crash recoveries, restart exhaustion and stopped-listener
checks passed again. This fixture does not prove customer build provenance or
connect the durable reservation pool to the service manager.

The fixture verified UID/GID 60000, no additional groups or capabilities,
no-new-privileges, denied release/system writes, denied root-only canary reads,
writable bounded temporary mounts, and the configured cgroup limits. A reachable
non-loopback IPv4 endpoint became unreachable from the service. Loopback HTTP
worked, including after two forced main-process crashes. A third crash exhausted
the start limit and left the service stopped beyond its restart delay. Explicit
stop removed the listener. The temporary unit was removed and the VM was stopped.
Fixture files, the toolchain and its dedicated account remain inside that VM.

An earlier test expected `Result=start-limit-hit`; systemd retained `Result=signal`
while refusing the next start. The corrected check verifies restart count, failed
state, zero main PID and stable state beyond the restart delay instead.

## Remaining work

Host account reservation, service installation and service-manager fencing,
toolchain verification, authenticated activation, observed process identity, routing and
draining, bounded tenant log storage, and reboot/power-loss recovery remain.
This runtime profile still needs hostile workload qualification, including memory
and process exhaustion and cross-runtime access. Reading limit values is not that
qualification. Earlier build-profile stress results do not substitute for runtime
tests. No live VPS/Pi deployments or customer databases changed.
