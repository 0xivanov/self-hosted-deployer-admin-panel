# Static runtime process

This is a development runtime for one assigned static project. It has no portal database, fleet credentials or payment keys. Production rollout still requires the project assignment and reconciliation UI, certificate provisioning, network deployment and recovery qualification. Do not install it on the existing VPS/Pi fleet as an unreviewed deployment step.

Build with `go build -o static-runtime ./cmd/static-runtime`. Start with `static-runtime --config /private/runtime/config.json`.

The configuration is a regular mode-0600 JSON file, at most 16 KiB. Example structure (replace placeholders before use):

```json
{
  "root": "/private/runtime/site",
  "project": "PROJECT_ID_FROM_PORTAL",
  "token": "64_HEX_CHARACTERS_FROM_32_RANDOM_BYTES",
  "content_listen": "127.0.0.1:8793",
  "content_host": "demo.site.test:8793",
  "management_listen": "127.0.0.1:8792",
  "management_host": "127.0.0.1:8792",
  "content_cert": "/private/runtime/content.crt",
  "content_key": "/private/runtime/content.key",
  "management_cert": "/private/runtime/management.crt",
  "management_key": "/private/runtime/management.key",
  "spa": false
}
```

The project must be its exact 64-character hexadecimal portal ID. The token must match the publication worker assignment and must never be exposed in customer JavaScript. Keep content and management hosts/listeners distinct. The content host must be on a separate registrable domain from the customer portal before public hosting. Local examples require local DNS/hosts setup and certificates trusted by the requesting client. There is no insecure TLS flag or automatic certificate issuance.

Management binds only an explicit private or loopback IP, never a wildcard, hostname or public IP. For remote workers, provision a dedicated management network and firewall before selecting a private bind address. Do not connect customer runtimes to the existing fleet's private network. This check limits accidental exposure; routing/firewall policy remains an operator responsibility. Content may bind the operator-selected address. Both listeners require TLS; private keys must be regular private files. Certificates must cover their respective hostnames or IP addresses. Load replacement certificates by restarting the process; certificate rotation is not yet hot-reloaded.

The process validates configuration and loads certificates before listening. If either listener cannot start, it closes the other and releases the site directory. HTTP headers, reads, writes and idle connections have time limits. SIGINT/SIGTERM drains requests, closes both listeners and releases the runtime owner lock. Sources and the active revision survive restart. Filesystem durability and operating-system process-kill qualification still need testing on the production runtime platform.

The worker uses the management origin as its `endpoint`, the same `project` and `token`, and optionally a management CA file. Its only runtime routes are authenticated `POST /publish` and `GET /status`. The content listener does not expose these operations. A publication transport error is an uncertain outcome, not proof that nothing changed; retain the job and reconcile/retry the same revision. The panel's Publish button remains unavailable until assignment and reconciliation work is complete.
