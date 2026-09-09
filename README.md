# Deployer Admin Panel

A local browser console for operator-managed Self-Hosted Deployer environments. The Go server serves an embedded frontend and delegates live operations to the existing authenticated `deployer` CLI. No frontend build, Node runtime, new public API or server deployment is required.

## Start a demo

Requires Go 1.26.5 or later.

```sh
go run ./cmd/admin-panel --demo
```

Open **http://127.0.0.1:8787**. Demo apps, nodes, deployments and logs are simulated in memory. Update `hello-world`, change its image or replica count, deploy, and select **Restore previous configuration** to demonstrate rollback. Demo mode never connects to live infrastructure.

## Connect to your environment

Install a current Deployer CLI with named contexts, preflight, and `server_identity` in `deployer --output json server status`. The September 9 POC source supports these features. If you have the sibling repository checked out:

```sh
mkdir -p bin
(cd ../self-hosted-deployer && go build -o ../self-hosted-deployer-admin-panel/bin/deployer ./cmd/deployer)
go run ./cmd/admin-panel --deployer ./bin/deployer
```

The default is **read-only**. The panel reads the usual private Deployer configuration and selected context. Choose another context or config explicitly:

```sh
go run ./cmd/admin-panel --context customer-a --config /absolute/private/config.json
```

To enable real deploy/update and session rollback:

```sh
go run ./cmd/admin-panel --context customer-a --allow-writes
```

`--deployer` selects the CLI executable; `--port` changes the local port. Credentials can come from the existing inline token or `env:` / `file:` context credential references. Credential and config files must be private regular files, normally mode `0600`.

## Features

- App inventory, desired replica counts, runtime details and warnings.
- Worker inventory and connectivity state. This lists Deployer worker records, not every Kubernetes control-plane node.
- Deployment configuration and history.
- Last 200 log lines per Pod, loaded on demand.
- YAML deployment/update with backend preflight and an explicit environment confirmation.
- Restore the configuration captured before a successful update in this panel session.
- Clear read-only and simulated-demo modes.

## Scope and safeguards

The server binds only to `127.0.0.1`. It is a local operator console, not a public multi-user portal. Do not expose it through a tunnel or reverse proxy. Live control-plane connections require HTTPS; the browser never receives the administrator token. The selected endpoint and credentials are copied into a private temporary context, pinned to the observed server identity and removed on graceful shutdown. Changing another CLI context cannot silently retarget an open panel. Abrupt termination may leave a private `deployer-admin-*` temporary directory for manual cleanup.

API requests require a per-process random session header. Host/origin checks, no cross-origin access, no-store responses and a restrictive Content Security Policy protect the local browser boundary. Requests and log output are bounded. Live requests still use the backend's authentication and mutation audit controls.

Rollback restores application configuration, not database contents, volumes or historical container artifacts. Only updates made during the current panel process create rollback snapshots. Historical deployment rows do not contain the old YAML. A changed desired configuration blocks a stale restore; an external update concurrent with the final deploy cannot be made atomic because the current backend API has no revision precondition. Image tags may change upstream; use image digests when exact artifact restoration matters. A successful deploy response means the backend accepted/applied the configuration, not that rollout readiness has been verified. Refresh the runtime details afterward.

There is no customer self-service, billing, secret editor, node provisioning, persistent rollback archive or production authentication portal in this initial UI.

## Build and validate

```sh
go test -race ./...
go vet ./...
go build -o bin/admin-panel ./cmd/admin-panel
node --check internal/adminui/static/app.js  # optional JS syntax check
```

Tests cover cross-origin and rebinding rejection, missing sessions, read-only mode, identity mismatch, update/rollback, external-change conflicts, failed deploys, request/output bounds and frozen private CLI configuration. Browser verification exercised demo details/logs/update/rollback and the live read-only inventory, status and log endpoints. No live deployment was changed by these panel checks.
