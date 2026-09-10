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
