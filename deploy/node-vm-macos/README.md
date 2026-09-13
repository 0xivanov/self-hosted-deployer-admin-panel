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

Still required: a trusted builder image/boot job, payload staging, immutable output
snapshot acquisition, safe artifact export and integration with the durable
executor provider. The optional disk channels described below still need a production guest boot
job and executor integration; this is not yet a complete `NodeBuildExecutor`.


## Build disk channels

With `--build-disks`, the private operation directory must also contain
`input.iso` (2 KiB to 128 MiB) and `output.disk` (64 to 512 MiB). Both must be
private owned regular files with one hard link, and sizes divisible by 512.
The launcher attaches the input read-only and output read/write. Use stable device
IDs rather than assuming Linux disk-letter order:

- `/dev/disk/by-id/virtio-deployer-input`: read-only build input ISO.
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
