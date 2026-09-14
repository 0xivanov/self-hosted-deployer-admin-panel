# Disposable Mac build VM launcher

`main.swift` uses Apple's Virtualization framework on Apple Silicon macOS 13+.
It starts one Linux EFI VM with 2 CPUs, 2 GiB RAM, a private raw disk and EFI store.
There are no network devices, shared directories, sockets or host guest-agent
channels. Build-disk mode captures an output-only guest console for diagnostics. A deadline or SIGINT/SIGTERM requests a forced VM stop.

Build and sign locally with the virtualization entitlement:

```sh
swiftc -target arm64-apple-macos13.0 -framework Virtualization \
  deploy/node-vm-macos/main.swift -o /private/tmp/node-vm-macos
codesign --force --sign - \
  --entitlements deploy/node-vm-macos/entitlements.plist /private/tmp/node-vm-macos
```

Usage: `node-vm-macos PRIVATE_OPERATION_DIRECTORY EXECUTION_ID SECONDS [--build-disks]`.
The duration must be 1 to 900 seconds and the execution ID 64 lowercase hex
characters. The canonical operation directory must be owned by the calling user
and private (0700). It must contain private, regular, singly-linked `disk` and
`efi` files, copied from a stopped, trusted template. Never pass a live VM disk,
template originals, customer-selected paths or files shared with another run.
The controller must map each execution ID to one permanent operation directory.

The launcher exclusively creates and syncs `attempt.json` before starting the VM.
It records `running.json` after framework startup succeeds and `stopped.json` only
after the framework reports a stopped VM. The attempt file permanently prevents
another launch in the same directory. Preserve all records after crashes or
errors; deleting a record to retry can invalidate delayed-operation fencing.
An absent stopped record means reconciliation is required, not that a build is
still running or that execution has safely retired. The lifecycle records alone
do not authorize release publication and do not establish build success.

The focused local rehearsal used private APFS clones of the stopped disposable
Linux lab's disk and EFI store. The VM started, ran with zero configured network
devices and stopped 0.012 seconds after its 15-second deadline. A repeat invocation
was rejected by the persistent attempt record. No live hosting node changed.

Still required: reproducible trusted image provisioning, controller payload staging,
controller-managed immutable snapshot acquisition and integration of the export
stage with the durable executor provider. The boot job below implements guest execution;
this is not yet a complete `NodeBuildExecutor`.


## Build disk channels

With `--build-disks`, the private operation directory must also contain
`input.iso` (2 KiB to 128 MiB) and `output.disk` (64 to 512 MiB). Both must be
private owned regular files with one hard link, and sizes divisible by 512.
The launcher attaches the input read-only and output read/write. Use stable device
IDs rather than assuming Linux disk-letter order:

- `/dev/disk/by-id/virtio-deployer-input`: read-only build input filesystem image.
- `/dev/disk/by-id/virtio-deployer-output`: size-limited build output disk.

The first configured disk is the disposable operating-system image. In build-disk
mode, `console.log` retains at most 1 MiB of guest serial output. Excess bytes are
drained and discarded. Console text is untrusted diagnostic data and must never
be interpreted as controller instructions or proof of successful execution.

No host folder is shared. The controller prepares input files and an empty output
disk before launch and must not mutate or inspect guest output while execution is
running. After confirmed VM stop it must acquire an immutable output snapshot and
inspect/export it in isolation. Do not mount an untrusted filesystem directly in
the Mac's host kernel. The launcher does not format filesystems or infer build
success from disk contents.

The synthetic `deploy/qualification/node-disk-channel-probe` fixture service runs
only when the identified input disk has label `NODEBUILD_FIXTURE`. It checks guest disk read-only
flags, reads a fixed marker from the ISO, verifies that an actual input block
device write is rejected, writes a different fixed marker to the output disk and
requests poweroff. It is a test fixture, not a customer build boot service.


On September 13 the focused guest check passed in 5.006 seconds: read the input,
reject an actual block-device write, write the expected output marker and power
off. The host then confirmed the VM was stopped, the input SHA-256 was unchanged
and the output marker persisted. An open-for-writing check alone was insufficient
on Linux, so the fixture now tests the actual write operation.

## Automatic guest build job

Provision `guest-build.py` at `/opt/deployer-build/guest-build.py` (root-owned 0644),
`guest-build.service` in `/etc/systemd/system/` and enable the service in a trusted
Linux template. Install the Linux `node-build-guest` binary at
`/opt/deployer-build/node-build-guest` (root-owned 0755). The template needs Python 3,
systemd, UDF support, e2fsprogs, a dedicated UID/GID 60000 with no extra groups,
and the pinned Node toolchain under `/opt/deployer-node/toolchains/<SHA256>/bin`.
Do not provision credentials into the template. Stop it before cloning.

The job skips normal template boots without the `DEPLOYER_BUILD` input label.
Build input uses **UDF**, despite the transport filename `input.iso`, to preserve
long dependency bundle and tarball names. The input root contains `source.zip`,
the verified `dependencies-<digest>` directory and `request.json`. The request
contains exactly `ExecutionID`, `Plan`, `ToolchainSHA256`, `Bundle`, and `NotAfter`
(a Unix deadline at most 60 seconds ahead). Plan and Bundle use the existing Go
guest helper JSON formats. No request-supplied filesystem paths are accepted.

The job validates device access flags and output capacity, rejects output disks
with existing filesystem signatures, privately copies bounded regular input files,
and formats the blank output as ext4. It runs the existing offline Go builder
under the dedicated user, with no network, a read-only system/input tree, private
temporary storage, 512 MiB memory, 128 tasks, one CPU worth of time and a deadline.
A fresh `/var/lib/deployer-build` is required, preventing reuse of a used OS image.

Output contains `work/`, private capped `build.log` and `result.json` identifying
the execution and candidate source tree. The job requests shutdown on success or
failure. A completed transient service may already have been collected by systemd;
this does not replace host-confirmed VM retirement. Logs and result records remain
untrusted. Only after the host confirms stop may a separate isolated exporter
inspect an immutable output snapshot and validate a release archive. Never mount
the output in the Mac host kernel or publish a release based on console text.

The September 13 synthetic Node build reported success using these disk inputs
and boot job. The host confirmed zero network devices and VM stop roughly 37.3
seconds after startup. This was a dependency-free project; output artifact content
was not independently exported or activated. The disposable template is stopped.

## Isolated stopped-output export

`--export-disks` adds a fourth read-only disk, `snapshot.disk`, with stable ID
`deployer-snapshot`. It has the same private-file rules as other operation files
and must be 64 to 512 MiB, aligned to 512 bytes. Export mode retains the capped
console and uses zero network devices. Existing build mode is unchanged.

After confirming the builder is stopped, the controller must create a separate
private copy of its output disk, prevent further writers, and compute its SHA-256.
Use a fresh trusted OS/EFI copy and a new export operation directory. Never boot
the builder's used OS disk as the exporter. Do not mutate the snapshot during or
after export; attach it read-only. A filesystem copy or digest alone is not proof
that the builder has stopped. Preserve execution mappings and lifecycle records.

Provision `guest-export.py` at `/opt/deployer-build/guest-export.py`, its enabled
service in `/etc/systemd/system/`, and the Linux `node-artifact-export` binary at
`/opt/deployer-build/node-artifact-export`. Files must be root-owned; scripts and
units 0644, binary 0755. The fresh exporter image needs Python, systemd, UDF and
ext4 support, but no Node toolchain or customer credentials. It must contain no
`/var/lib/deployer-export` directory from an earlier run.

Its input UDF label is `DEPLOYER_EXPORT`. `request.json` contains exactly
`ExecutionID` (the original build ID), `SourceSHA256`, `SnapshotSHA256`, and
`NotAfter` (Unix deadline at most 60 seconds ahead). The launcher's operation ID
identifies the separate exporter VM, not the original builder. The controller
must bind those two operations. The output is a separate blank raw disk.

The export boot job checks the snapshot digest, mounts ext4 read-only with journal
replay and execution disabled, and checks the candidate build metadata against
the assigned source/execution. The existing bounded Go exporter validates paths,
symlinks, sizes and ZIP contents. The job writes ZIP bytes at offset 4096 and
publishes a NUL-padded JSON header in the first 4096 bytes only after syncing the
archive. The header contains `Version: 1`, `ExecutionID`, `SourceSHA256`,
`SnapshotSHA256`, `ArchiveSHA256` and `ArchiveBytes`. Remaining disk bytes are not
part of the archive. The header and disk contents remain untrusted guest output.

After independently confirming the export VM has stopped, use:

```sh
go run ./cmd/node-artifact-import STOPPED_EXPORT_DISK NEW_ARCHIVE_PATH \
  EXPECTED_BUILD_EXECUTION_ID EXPECTED_SOURCE_SHA256 EXPECTED_SNAPSHOT_SHA256
```

The destination parent must be private. The importer reads raw bytes without
mounting the disk, checks the framing and expected identities, and independently
validates the ZIP and digest before exclusively creating the private archive.
It does not infer VM retirement, resolve tenant ownership, or authorize release
retention. The durable controller must supply identities from its own assignment,
not copy them from the returned header, and must enforce both VM stop gates.
