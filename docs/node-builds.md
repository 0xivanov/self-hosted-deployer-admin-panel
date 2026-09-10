# Node.js build implementation

The initial Node target is Linux on an explicitly assigned amd64 or arm64 runtime,
using Node 24 LTS and npm. The exact Node/npm image digest must be pinned and
qualified before execution. Native dependencies must be built for the same OS,
architecture and toolchain as the runtime. The existing Lima control-plane lab is
not a qualified customer build sandbox.

## Implemented preflight

`internal/nodebuild.Prepare` revalidates the uploaded ZIP, verifies its expected
SHA-256 and derives versioned build instructions without extracting or executing
anything. The archive must contain a root package.json with a start script and a
supported package-lock.json. An explicitly declared alternative package manager is
rejected. npm itself must validate lockfile/package consistency during installation.

The `node-build-plan` command accepts `--source`, `--sha256`, `--architecture`
(amd64 or arm64), and optional `--skip-build`. It prints a JSON plan containing the
source digest, Linux architecture, Node major version, commands and runtime port.
It is a preflight tool only. Generated commands must never be executed directly on
the portal, Mac or legacy fleet. The source path is operator-local, never supplied
as a browser path.

Build steps are npm ci with development dependencies and strict engine checks,
optional npm run build when defined, then offline pruning of development packages
without lifecycle hooks. Start uses npm start with pre/post hooks disabled; the
start script itself still runs. npm installation hooks and build scripts are
untrusted execution and remain enabled inside the future isolated builder so that
native dependencies can work. Script contents never enter generated shell text.

## Remaining executor and runtime requirements

- A disposable VM per build, with no host mounts, SSH agent, infrastructure secrets,
  runtime secrets or legacy VPN. No execution until this boundary is qualified.
- A pinned toolchain and clean allowlisted environment. Build HOME/cache must be
  job-local; no inherited npm configuration, proxy variables or credentials. Runtime
  NODE_ENV=production, HOST=0.0.0.0 and PORT=3000; the application must honor PORT.
- VM-level network controls preventing metadata, private networks, control-plane
  access and arbitrary egress. Public package downloads need an approved gateway.
  String checks of dependency URLs are not a network isolation boundary.
- CPU, memory, disk, PID, wall-time and log limits, cancellation, cleanup and recovery
  after an uncertain worker outcome. npm hooks can spawn descendants.
- Durable owner/developer-authorized build jobs with source/plan/toolchain identity,
  leases and bounded artifact/log retention. Recheck permissions before dispatch.
- Export a bounded, verified runtime artifact from a stopped builder. Reject unsafe
  paths/symlinks and preserve necessary native module permissions explicitly. Never
  import build-generated Dockerfiles or arbitrary runtime manifests.
- Dedicated workspace runtime, health probes, environment secrets, logs and durable
  rollback. A failed build or unhealthy candidate must leave the old release serving.

Validated preflight tests cover source identity, optional/disabled build scripts,
malicious script text, unsupported architecture/package manager and cancellation.
They do not establish execution isolation or a working Node deployment.

References checked on 2026-09-10:

- https://github.com/nodejs/Release (Node 24 LTS release schedule)
- https://docs.npmjs.com/cli/v11/commands/npm-ci/ (lockfile installs, engine/config
  behavior and the distinction between lifecycle hooks and explicit script commands)

## Source extraction

`nodebuild.ExtractSource` accepts the exact ZIP, its assigned SHA-256 and an open
operator-controlled private job root. It revalidates the archive before writing,
creates a random private source directory and confines all writes to Go root
handles. Files are owner-readable/writable; uploaded executable helpers retain
owner execution only. Archive ownership and group/other permissions are not imported.

The returned directory is relative to the retained job root. The caller owns
cleanup after success and must keep the directory inaccessible to customer code
until extraction completes. Ordinary errors and cancellation remove partial output.
A process crash can leave a partial directory; the future durable worker must
reconcile and remove abandoned job directories before retrying. Extraction is not
a durable publication operation and does not start a VM, install dependencies or
execute scripts. Source directories must never be publicly served.

Integration tests use disposable local directories and synthetic ZIPs. They cover
independent extraction paths, content/digest identity, private permissions, nested
executable helpers, traversal/symlink rejection and cancellation after a file has
been created. These checks qualify extraction behavior, not VM isolation.
