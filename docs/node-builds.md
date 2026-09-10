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

## Durable build requests

Schema 16 adds project-scoped Node build records. `RequestNodeBuild` checks current
owner/developer access, resolves an upload belonging to that exact Node project,
revalidates it and saves the generated plan with an operator-assigned toolchain
SHA-256. It never accepts browser-supplied command text. The future executor must
match the toolchain content digest as well as the saved Linux architecture.

A request key is unique within the project. Retries return the same saved request;
changed uploads, effective plans or toolchains conflict. Only one queued/running
build can exist per project. History is currently capped at 100 records per project.
Build records retain source uploads even after cancellation; archival and release-
aware retention must be implemented before public launch. This is deliberately
bounded rather than silently discarding source identity needed for recovery.

Owners/developers can list build details and cancel queued requests. Cancellation
is idempotent, but running work cannot be relabelled cancelled through this method.
The executor must first stop and reconcile its VM before a future cancellation
protocol can complete. Viewer access to build details is denied.

No HTTP build routes, VM dispatch or runtime activation are implemented yet.
Worker claims and leases are described below. A queued database record does not mean code is executing. Worker
permission checks at dispatch, pinned toolchain qualification, durable completion,
log/artifact storage, cancellation and crash recovery remain required.


## Worker claims and leases

Schema 17 adds a unique execution identity and hashed, expiring lease. Trusted
workers claim only queued jobs for an operator-assigned project, exact toolchain
SHA-256 and architecture. The claim checks the original submitter's current verified,
enabled account and owner/developer membership, and hashes the source bytes again.
It persists the execution identity before returning the source to the worker.
Archive contents and the raw lease are excluded from JSON serialization.

Leases last one minute. Renewal requires the exact job, execution identity and
unexpired lease, and repeats permission checks. Workers must renew immediately
before dispatch and periodically while working. Losing permission or the lease
requires stopping and reconciling the VM; a failed renewal alone cannot stop a
remote process. The executor must use execution identity for durable VM lookup and
idempotent dispatch, with no undiscoverable fire-and-forget create operation.

Running jobs, including expired ones, are never automatically reclaimed or made
available for another build. This prevents an uncertain remote VM outcome from
starting a duplicate. It also means a future reconciliation path is mandatory:
look up the exact VM execution, establish its terminal state, retain verified
artifacts or failure evidence, and only then finish/release the pending job.
There is no VM execution or successful-artifact completion method yet.
Failed/cancelled execution reconciliation is described below.


## Reconciling failed or cancelled executions

Schema 18 retains executor observations for terminal failure/cancellation.
`ReconcileNodeBuildFailure` is trusted worker access and only inspects running
jobs with expired leases. A provider observation must be fresh and match the
execution ID, source digest, toolchain digest and architecture. The executor must
explicitly attest that the operation is retired, including any delayed create or
restart. A stopped or missing VM alone cannot satisfy that condition.

A matching retired failure/cancellation is recorded atomically with the terminal
job state and lease removal. This allows a new explicit build request without
silently redispatching the old one. Provider errors, stale/mismatched evidence,
active leases, nonretired operations and success without artifact qualification
leave the pending build unchanged. Already-recorded outcomes are idempotent and
remain available across restart. Cleanup reconciliation does not depend on the
submitter retaining customer access, so a disabled account cannot strand its VM.

The provider interface currently has synthetic integration coverage only. A real
executor must implement durable operation tombstones/fencing before claiming
`Retired=true`. The failed-build path cannot mark a build successful, publish an
artifact or activate hosting. No executor transport or VM was started here.

A trusted synthetic build/start/stop rehearsal has now passed in a disposable
ARM64 Linux VM. See [the recorded environment, checks and limitations](node-build-rehearsal.md).
This does not enable public Node execution or replace the remaining isolation gates.
