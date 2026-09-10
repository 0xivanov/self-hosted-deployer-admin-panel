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
are rejected, including after retirement. New operations cannot alias another
operation's release directory, even after that operation retires.
`ClaimStart` requires a matching installation receipt and changes `reserved` to
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

## Installation receipts and interrupted attempts

Pool schema 2 adds an installation-attempt marker and an immutable receipt.
`PrepareRelease` validates the archive against the reservation, commits the attempt
before calling the trusted installer, then checks the returned directory and full
manifest before persisting success. A successful retry returns the same receipt.
Missing receipts block `ClaimStart`. Timeout, malformed result, provider error or
process exit after dispatch leaves the attempt recorded and cannot trigger another
installation. These unknown outcomes retain their slots for recovery/retirement.
An invalid archive rejected before dispatch does not consume the attempt.

`LinuxInstaller` implements archive installation at the reserved directory with
the same read-only sealing and no-replace publication as `InstallRelease`. It also
checks the actual machine architecture. The host manager must still prevent
delayed installer calls after retirement and protect installed bytes for their
whole lifetime. The receipt proves installation succeeded; it is not a current
process observation or a substitute for toolchain/service-manager qualification.

Schema-1 migrations preserve outstanding states. Previously started operations
remain started without invented receipts and cannot be installed or started again.
The older binary rejects schema 2; no live customer database was migrated.

Tests passed competing preparation calls, retirement during installation, receipt
identity/size failures, process exit inside the installer, receipt persistence,
directory alias rejection and migration of an outstanding schema-1 start. A real
Linux-root test connected reservation, `LinuxInstaller`, receipt persistence and
start authorization, and verified that even a direct second installation cannot
replace the reserved release. Production service-manager dispatch remains to be
connected; recovery of an unknown installation is described below.

## Recovering a published installation without a receipt

`ReconcileInstallation` requires an existing attempted, still-reserved operation
and its original validated archive. It requests fresh local evidence, checks the
entire assignment and manifest, then records the receipt only if the operation is
still eligible. A concurrent retirement cannot be undone. A persisted receipt
makes retries idempotent while the operation remains reserved. Recovery does not
install files or start a process, and failed verification does not free the slot.

`nodeartifact.VerifySealed` compares the complete installed tree to the archive:
all expected and implicit directories, file contents, exact internal symlink
targets, executable permissions and read-only modes. Missing and extra paths are
rejected. `LinuxInstaller.ObserveNodeInstallation` additionally checks root
ownership, the actual machine architecture, a real release directory rather than
a directory symlink, and single-link regular files. It syncs files and directories
before returning a locally timestamped observation. It never repairs/re-seals
drift to make verification pass. The runtime agent must keep the tree protected
against concurrent mutation and later service-manager operations.

On 2026-09-10, the disposable Linux-root test exited a child process immediately
after publishing the release, before its receipt was saved. The parent reopened
the pool, confirmed starts remained blocked, recovered the receipt through the
actual Linux verifier, and then obtained start authorization. Exactly one release
directory remained. Additional tests rejected changed bytes, missing/extra paths,
changed links or modes, wrong ownership, directory symlinks, hard links, stale or
mismatched observations and retirement races. The VM was stopped afterward.

For recovered receipts, `InstalledAt` records successful recovery verification,
not a guessed original publication time. This proves this process-exit recovery
path, not power-loss durability, host compromise resistance or complete runtime
recovery. Damaged/absent installations still require retirement and a new operation.

## Durable Linux control gate

`ControlGate` uses a permanent per-operation lock file and Linux `flock` to
serialize mutations across controller processes. Private root-owned JSON records
retain the exact assignment and installation/start attempts. Each intent is synced
before invoking the trusted action, so a process exit or lost response cannot
authorize another installation/start. The gate deliberately records attempts, not
successful service state. Pool receipts and start authorization remain required.

Retirement commits a permanent fence before invoking cleanup, including when no
installation has arrived yet. Cleanup can be retried after failure, but delayed
install/start requests remain rejected. The lock is held through each callback;
callbacks must honor their deadlines and must not reenter the same operation's
gate. `GatedInstaller` connects pool preparation and recovery to this gate.

Every controller must share the same durable gate root and use it for these
mutations. Never delete lock files, prune retirement records or restore an older
gate snapshot while requests/services could survive. Files require root ownership,
0600 permissions and one hard link; the gate root requires 0700. Record parsing is
bounded and strict. A regression test found that `os.Root.OpenFile` followed a
symlink despite the supplied flag; gate files now use direct `openat` with kernel
`O_NOFOLLOW` against the pinned root directory descriptor.

Linux-root tests passed controller contention/cancellation, process exit during
start, repeated/failed cleanup, retirement before install, identity conflicts and
unsafe record rejection. A separate connected test exercised pool preparation,
actual read-only installation, start authorization and the retirement gate.

The runtime rehearsal now uses the same gate for actual systemd start/stop. On
2026-09-10 operation
`08afd5e301d242e3ab55f4737697c377817ac921a8eb4d32a2eee92b9a6373e6`
passed HTTP, permissions/resource/network checks, two crash recoveries, restart
exhaustion, stopped-listener checks and rejection of a later start request.
The fixture unit was removed. Gate records remain under
`/var/lib/deployer-node-lab/control` inside the stopped disposable VM.

The qualification adapter is not the production service manager: it uses a fixed
trusted lab assignment and pre-staged toolchain. The production adapter still needs
verified toolchain/account provisioning, service-specific stop/mask and queued-job
settlement, full process/listener observations, routing detach/drain coordination
and boot recovery. Cleanup must target the immutable operation/service identity,
not blindly kill a UID that another operation could later reuse. A gate retirement
record by itself never proves processes or systemd restart paths are gone and must
not be used alone to release a pool slot.

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
