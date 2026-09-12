# Disposable Mac build VM launcher

`main.swift` uses Apple's Virtualization framework on Apple Silicon macOS 13+.
It starts one Linux EFI VM with 2 CPUs, 2 GiB RAM, a private raw disk and EFI store.
There are no network devices, shared directories, sockets, serial ports or host
guest-agent channels. A deadline or SIGINT/SIGTERM requests a forced VM stop.

Build and sign locally with the virtualization entitlement:

```sh
swiftc -target arm64-apple-macos13.0 -framework Virtualization \
  deploy/node-vm-macos/main.swift -o /private/tmp/node-vm-macos
codesign --force --sign - \
  --entitlements deploy/node-vm-macos/entitlements.plist /private/tmp/node-vm-macos
```

Usage: `node-vm-macos PRIVATE_OPERATION_DIRECTORY EXECUTION_ID SECONDS`.
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
executor provider. The current launcher has no customer payload or output channel
and is not yet a complete `NodeBuildExecutor`.
