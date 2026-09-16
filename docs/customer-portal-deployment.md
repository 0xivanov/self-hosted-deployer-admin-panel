# Customer portal deployment

The customer portal runs independently from the operator panel as Linux user
`launchstead-portal`. Install the two binaries at `/opt/launchstead-portal`:
`customer-portal` and `billing-worker`. Keep the SQLite database at
`/var/lib/launchstead-portal/portal.sqlite` and create `/etc/launchstead-portal`
for the private configuration and TLS files.

Build locally, copy the binaries to the VPS, then install the units from
`deploy/customer-portal.service` and
`deploy/launchstead-portal-billing-worker.service`. The units enable HTTPS on
the VPN address `10.8.0.1:8791`, enable email-verified signup and test billing, and run both processes as
`launchstead-portal`.

Create these mode `0600` files on the VPS, owned by `launchstead-portal`:

* `tls.crt` and `tls.key`, the origin certificate and private key.
* `billing-test.json`, for `--test-billing-management-config`. It contains the
  portal management fields `secret_key`, `success_url`, `cancel_url`, `plans`,
  and `configuration`.
* `billing-worker.json`, for `billing-worker --config`. It contains only the
  worker fields `secret_key`, `success_url`, `cancel_url`, and `plans`.
* `stripe-webhook-secret`, containing the test endpoint signing secret.
* `smtp.json`, containing the SMTP settings including relay `server_name`.
* `mail-key`, containing the durable 32-byte mail encryption key as 64 hex characters.

The two billing JSON files must be separate because the worker rejects the
portal management `configuration` field. Keep all values local and use
`https://portal.0xivanov.dev/billing/success` and
`https://portal.0xivanov.dev/billing/cancel` as the return URLs.

Before starting the services, create the user and directories, make sure the
database is backed up consistently, and validate the units:

```sh
sudo install -d -o root -g root -m 0755 /opt/launchstead-portal
sudo install -d -o launchstead-portal -g launchstead-portal -m 0700 \
  /etc/launchstead-portal /var/lib/launchstead-portal
sudo systemd-analyze verify deploy/customer-portal.service \
  deploy/launchstead-portal-billing-worker.service
sudo install -m 0644 deploy/customer-portal.service /etc/systemd/system/
sudo install -m 0644 deploy/launchstead-portal-billing-worker.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now customer-portal launchstead-portal-billing-worker
```

Apply `deploy/customer-portal-ingress.yaml` after the binaries are listening.
Bootstrap the origin trust Secret from the public origin certificate, never its
private key, then apply the manifest:

```sh
sudo k3s kubectl create namespace launchstead-portal --dry-run=client -o yaml | sudo k3s kubectl apply -f -
sudo k3s kubectl create secret generic customer-portal-origin-ca \
  -n launchstead-portal --from-file=ca.crt=/etc/launchstead-portal/tls.crt \
  --dry-run=client -o yaml | sudo k3s kubectl apply -f -
sudo k3s kubectl apply -f deploy/customer-portal-ingress.yaml
```

The Traefik `ServersTransport` verifies the VPS certificate using that Secret
and the certificate name `portal.0xivanov.dev`; it does not disable TLS
verification. Confirm that the origin certificate includes that identity before
applying the route. DNS for `portal.0xivanov.dev` must point to the Traefik
entrypoint. Check service logs and then qualify login, test checkout, webhook
receipt, and billing reconciliation in Stripe test mode.

This deployment does not change the existing admin panel, control-plane
services, application workloads, DNS, or secrets.

## Deployment evidence, 2026-09-16

The portal and test billing worker are running on the VPS. The public origin is https://portal.0xivanov.dev. Let's Encrypt issued the public certificate; Traefik separately validates the private origin certificate. The origin certificate expires 2027-09-16 and requires replacement and updating the origin CA Secret before that date.

The Starter test plan uses the existing EUR 5/month recurring price. The worker successfully fetched its price observation. The test billing portal permits invoice history, payment-method updates and period-end cancellation. The Stripe endpoint `/webhooks/stripe-test` pins API version `2026-08-26.dahlia`.

An unpaid test Checkout Session was created and immediately expired. Stripe delivered `checkout.session.expired` event `evt_1UGFauKrW0QYIgssu9d86ioO`; the application verified its signature and stored it durably. Because this transport-only fixture has no local workspace checkout, the operator marked that exact inbox event ignored and its work complete after recording receipt. No payment was submitted. Unsigned webhook requests returned 400. The portal returned 200 with certificate validation enabled. Both existing operator services remained active. Focused billing-management and webhook integration tests passed, as did systemd unit verification.

Signup remains disabled and no customer accounts were provisioned. Before user onboarding: configure account email, enable controlled signup, enroll a test workspace and complete a mapped checkout/subscription/management flow. Add this separate database and its encryption/signing settings to the backup and recovery procedure before storing customer data. Merchant Connect sales, live payments and website runtime workers are not enabled by this deployment.

To roll back this addition, disable and stop `customer-portal` and `launchstead-portal-billing-worker`, remove only the `launchstead-portal` ingress, and disable its Stripe test endpoint. Preserve `/var/lib/launchstead-portal` and private configuration. Do not alter the existing operator services or application namespaces.

## Account email and backups, 2026-09-16

Account mail uses the existing Gmail alert credentials through the cluster TCP relay at `10.43.128.251:587`. SMTP configuration explicitly sets `server_name` to `smtp.gmail.com` so STARTTLS and authentication verify Gmail's identity, independently of the relay dial address. No certificate validation is disabled. Credentials remain in `/etc/launchstead-portal/smtp.json`, mode 0600; the durable outbox encryption key is `/etc/launchstead-portal/mail-key`, also 0600. Neither file belongs in Git.

The portal unit enables signup, verification emails and password-reset emails. Registration requires email verification before login. Public signup is intended for the test preview only; live payments and customer runtime provisioning remain separate release gates. Existing account and shared-peer throttles still apply behind Traefik; proxy-aware limits and broader abuse controls remain unfinished.

The VPS recovery configuration now captures the portal SQLite database using the SQLite online-backup API, private configuration and mail key, binaries, and both service units. The pre-change recovery configuration is retained privately at `/etc/deployer/backup/recovery.before-portal.json`. A manual run of the existing offsite recovery backup completed successfully with the added sources. Restore the database together with private configuration, preserve file ownership and permissions, and verify outbox behavior before starting services.

Validation: `go test ./internal/portal` passed, including SMTP certificate checks and relay identity/validation tests. Focused `-tags integration` account lifecycle tests passed. After deployment `/api/config` reports `signup: true` and `account_mail: true`. First real mailbox receipt is awaiting operator confirmation; SMTP authentication success alone does not establish inbox delivery.
