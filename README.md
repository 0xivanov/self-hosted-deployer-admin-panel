# Deployer Admin Panel

A browser console for operator-managed Self-Hosted Deployer environments. The Go server serves an embedded frontend and delegates live operations to the existing authenticated `deployer` CLI. No frontend build or Node runtime is required. Local mode needs no server deployment.

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

By default the server binds only to `127.0.0.1`. Remote mode explicitly requires HTTPS and a separate panel password. This is a single-operator console, not a multi-user portal. Do not expose local mode through a tunnel or reverse proxy. Live control-plane connections require HTTPS; the browser never receives the administrator token. The selected endpoint and credentials are copied into a private temporary context, pinned to the observed server identity and removed on graceful shutdown. Changing another CLI context cannot silently retarget an open panel. Abrupt termination may leave a private `deployer-admin-*` temporary directory for manual cleanup.

API requests require a per-process random session header. Host/origin checks, no cross-origin access, no-store responses and a restrictive Content Security Policy protect the local browser boundary. Requests and log output are bounded. Live requests still use the backend's authentication and mutation audit controls.

Rollback restores application configuration, not database contents, volumes or historical container artifacts. Only updates made during the current panel process create rollback snapshots. Historical deployment rows do not contain the old YAML. A changed desired configuration blocks a stale restore; an external update concurrent with the final deploy cannot be made atomic because the current backend API has no revision precondition. Image tags may change upstream; use image digests when exact artifact restoration matters. A successful deploy response means the backend accepted/applied the configuration, not that rollout readiness has been verified. Refresh the runtime details afterward.

There is no customer self-service, billing, secret editor, node provisioning, persistent rollback archive or multi-user authentication portal in this initial UI.

## Build and validate

```sh
go test -race ./...
go vet ./...
go build -o bin/admin-panel ./cmd/admin-panel
node --check internal/adminui/static/app.js  # optional JS syntax check
```

Tests cover cross-origin and rebinding rejection, missing sessions, read-only mode, identity mismatch, update/rollback, external-change conflicts, failed deploys, request/output bounds and frozen private CLI configuration. Browser verification exercised demo details/logs/update/rollback and the live read-only inventory, status and log endpoints. No live deployment was changed by these panel checks.

## VPS deployment

The service is available at `https://admin.0xivanov.dev` with **deployments and session rollback enabled**. Cloudflare DNS points directly to the VPS. The existing Traefik ingress serves a trusted Let’s Encrypt certificate managed and renewed by cert-manager. The original IP URL has been superseded.

Remote mode listens on all IPv4 interfaces at the selected port and requires all of:

```sh
admin-panel --public-url https://admin.0xivanov.dev \
  --tls-cert /etc/deployer-admin-panel/tls.crt \
  --tls-key /etc/deployer-admin-panel/tls.key \
  --auth-file /etc/deployer-admin-panel/auth.json --allow-writes \
  --config /etc/deployer-admin-panel/config.json \
  --deployer /opt/deployer-admin-panel/deployer
```

The private auth JSON contains `username` and `password`. Use a randomly generated password of at least 24 characters; it is distinct from the control-plane token. Authentication protects the HTML, assets and APIs; the browser session header and exact origin checks still apply. The public URL defines the exact browser origin independently of the listener port. Traefik preserves that Host header and uses HTTPS to reach the Go listener. The Go service requires actual TLS and does not trust forwarded headers as proof of HTTPS. Password rotation requires a service restart. HTTP Basic authentication has no application logout; close the browser session to clear cached credentials.

See [the systemd unit](deploy/deployer-admin-panel.service). Install the binaries under `/opt/deployer-admin-panel`, private credentials and TLS files under `/etc/deployer-admin-panel`, owned by the dedicated `deployer-admin` user. The service uses systemd sandboxing and starts on boot. Config and key files must be mode `0600`. Back up an existing binary before replacing it, restart only `deployer-admin-panel`, and verify both the unauthenticated 401 response and authenticated app inventory.

```sh
sudo systemctl status deployer-admin-panel
sudo journalctl -u deployer-admin-panel -n 30 --no-pager
sudo systemctl disable --now deployer-admin-panel # removes remote access
```

This deployment does not modify the existing control-plane binary or application workloads. It retains an administrator credential on the VPS for CLI operations. The deployed systemd unit explicitly enables writes with `--allow-writes`; remove that flag and restart the panel to return to read-only mode. The default for other installations remains read-only.

### Domain routing

[The ingress manifest](deploy/ingress.yaml) creates resources only in `deployer-admin`. It routes `admin.0xivanov.dev:443` to the VPS service on `10.8.0.1:8787`. The origin certificate is verified against the `admin-panel-origin-ca` Secret with `serverName: 159.195.146.26`; TLS verification is not disabled. Bootstrap the Secret from the origin's **public certificate**, never its private key:

```sh
sudo k3s kubectl apply -f deploy/ingress.yaml
sudo k3s kubectl create secret generic admin-panel-origin-ca -n deployer-admin \
  --from-file=tls.ca=/etc/deployer-admin-panel/tls.crt --dry-run=client -o yaml | sudo k3s kubectl apply -f -
```

The public certificate renews automatically through the existing `deployer-letsencrypt` ClusterIssuer. The separate self-signed origin certificate expires September 9, 2027; replace it and update the origin CA Secret before expiry. Replacing that certificate requires restarting the panel. Cloudflare API credentials are used locally to create DNS only, and are not installed on the VPS or committed here. The DNS record is DNS-only, not Cloudflare-proxied.

To revert the domain transition, restore `/opt/deployer-admin-panel/admin-panel.previous` and `/etc/deployer-admin-panel/service.previous`, reload systemd and restart the panel, then remove only the admin ingress resources and DNS record. This restores the previous IP URL and certificate warning.
