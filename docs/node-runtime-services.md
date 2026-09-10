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

Durable UID/port reservations, service installation and lifecycle records,
artifact sealing, authenticated activation, observed process identity, routing and
draining, bounded tenant log storage, and reboot/power-loss recovery remain.
This runtime profile still needs hostile workload qualification, including memory
and process exhaustion and cross-runtime access. Reading limit values is not that
qualification. Earlier build-profile stress results do not substitute for runtime
tests. No live VPS/Pi deployments or customer databases changed.
