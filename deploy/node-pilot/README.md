# Node.js public demo

This pilot connects the VPS customer portal to an isolated builder on the Mac
and a dedicated Node runtime in the `deployer-node-build-lab` Linux VM. Public
content is served at `https://node-demo.159-195-146-26.sslip.io`. The Mac must stay
awake, logged in and connected, and the lab VM must stay running. This is a demo
assignment, not automatic or always-on Node hosting.

The portal keeps its database on the VPS. Its build worker sends one assigned
project's validated ZIP and pinned build metadata over authenticated HTTPS to
the Mac executor. Dependencies are independently downloaded and verified against
the expected manifest. Customer build commands run only in fresh offline builder
VMs. A second isolated VM exports the stopped build output. The executor uses
controller stop records and archive validation before allowing release retrieval.

The deployment worker sends the retained ARM64 release to the Linux runtime over
private HTTPS. The runtime launches under dedicated UID 60010 or 60011, checks
`/health`, switches the route, and reports actual activation back to the portal.
Uploaded apps run with the existing memory/process/CPU/filesystem/network profile.
They have no portal, SSH, Stripe or Cloudflare credentials.

Mac private state is under
`/Users/ivanivanov/projects/.launchstead-private/node-demo`:
`pipeline.json`, frozen `template/`, `dependencies/`, permanent `executions/`,
TLS files and executor token. Never delete execution records to retry an uncertain
build. No secret contents belong in this repository. Template copies are kept
separate from the running Linux runtime VM.

The SSH launch agents `dev.launchstead.node-lab-tunnel` and
`dev.launchstead.node-vps-tunnel` forward loopback ports. Local 8941 is the build
executor; 8942 is runtime management; 8943 is runtime content. On the VPS,
loopback 8941/8942 reach those management endpoints and 8944 reaches content.
`node-demo-content.socket` accepts the cluster connection at 10.8.0.1:8943 and
forwards encrypted bytes to 127.0.0.1:8944. Traefik validates the origin
certificate, and cert-manager supplies the public certificate. No management
route is exposed through ingress.

The VPS runs `node-demo-build.service`, `node-demo-deployment.service`, and the
content socket/service. Install these alongside the checked-in ingress manifest.
The Linux lab runs `node-demo-runtime.service`. Private worker configurations are
`/etc/launchstead-portal/node-demo-build.json` and `node-demo-deployment.json`.
The portal uses `--node-projects /etc/launchstead-portal/node-projects.json` plus
its existing dynamic publication-site map. Each assignment pins the exact
project, runtime, architecture and toolchain. Do not use the live portal database
in a local worker or copy it to the Mac to process builds.

Stop new builds by stopping the VPS build worker. Drain the executor before
stopping its service or the Mac. Existing builds with uncertain interruption
require reconciliation; never resend an execution under a different identity.
The Node website depends on the runtime VM and tunnels even after a successful
build. Stopping this pilot does not require stopping the portal or static sites.
Public certificate renewal is automatic; private origin certificates require
operator rotation before September 2027. Full disaster recovery, automatic Node
provisioning, persistent app storage and general dependency/framework support
remain separate work.

## Mac executor

Build `go build -o /private/operator/node-build-executor ./cmd/node-build-executor`.
Run under the operator account with `--listen 127.0.0.1:8941 --host
127.0.0.1:8941`, `--project PROJECT_ID`, `--toolchain TOOLCHAIN_SHA256`,
`--architecture arm64`, `--token-file PRIVATE_TOKEN_FILE`, `--tls-cert CERT`,
`--tls-key KEY`, `--executions PRIVATE_EXECUTIONS`, `--dependencies PRIVATE_DEPS`,
`--pipeline-config PRIVATE_PIPELINE_JSON`, and `--pipeline-script
/absolute/repo/deploy/node-vm-macos/run-pipeline.py`. Token and config files must
be private; the two storage directories must be mode 0700. The pipeline's
DependenciesDirectory must match the executor flag. The Python executable
(default /usr/bin/python3) and script must not be group/world writable.

The executor holds a lifetime exclusive lock on its execution root. Persisted
requests fence duplicate launches, including after a restart. Conflicting
replays are rejected. Shutdown stops accepting work and drains the active
bounded pipeline. The installed launch agent is
`dev.launchstead.node-build-executor`, with a 300-second graceful exit window.
Uncertain incomplete executions require operator reconciliation, not retries.
The fixed preview does not automatically clean up VM disks or cap aggregate disk
usage, so monitor local storage and preserve stop/evidence records before cleanup.

Verified 2026-09-16: a source ZIP uploaded through Brave built via the live VPS
worker and isolated Mac pipeline. Both VMs stopped, the portal retained the
release, and Deploy activated Node revision 1. The public HTML exactly matched
the starter page, /health returned `ok`, and the demo button worked. Existing
static sites stayed available. VPS backup covers portal jobs/releases and worker
configuration/units. Mac VM disks and runtime state are not included in that
VPS backup; this pilot is not a qualified disaster-recovery setup.
