# Live billing implementation and activation

September 25, 2026. Production still uses Stripe test mode. Live payments are
not ready merely by supplying a live secret key.

## Provider foundation

The hosting provider now exposes explicit `NewLiveClient` and `VerifyLiveEvent`
entry points. Existing test constructors and verification retain their test-only
behavior. Keys must match the selected mode. Checkout/customer responses,
prices, subscriptions and nested invoice/price objects, charge and dispute
observations, invoice payment discovery and billing-management configurations
are checked against that mode. Persistent request keys and fixed return URLs
remain mandatory. Runtime callers can now select the live entry points explicitly; production configuration has not changed.

Focused integration checks cover matching/mismatched keys and checkout responses,
signed event mode isolation, mixed-mode nested subscription objects, and the
existing provider suite. All network responses in these checks are synthetic;
no real customer, checkout or charge was created.

## Hosting persistence deployed

Schema 55 separates customers, plans, checkouts, subscriptions, charges, events,
worker tasks and plan limits by billing mode. Existing schema-54 records migrate
to test mode. Composite bindings prevent live records from referencing test
parents; reads, refreshes, deduplication, worker leases and hosting entitlements
use the selected mode. Existing sandbox evidence cannot authorize live hosting.
Authenticated provider clients declare their mode and mismatches are rejected
before provider calls or work leasing.

Runtime mode is immutable for each opened store. Existing `Open` callers and
legacy test flags retain test behavior. Production runs admin `417691c`, schema 55,
with test billing. The coordinated rollout verified all eight installed consumer
binaries and preserved every original billing row in test mode. The root-private
rollback snapshot is `/var/backups/launchstead-portal-billing55-20260925`. Restore
matching databases, binaries and settings together; old binaries must not run
against the upgraded database. See [production upgrade](production-upgrade-20260925.md).

Verification passed for billing, hosting and historical database migrations,
including same-ID event isolation, wrong-mode signed webhook rejection, sandbox
queue isolation and legacy payment history preservation. The provider integration
suite, Go vet for the affected packages and all command builds also passed.
Provider responses are fixtures; no live transaction was performed.

Runtime checks also cover a simulated live customer/checkout/webhook/subscription
flow, the live webhook route rejecting sandbox events, CLI flag conflicts,
legacy test configuration, new live registrations requiring payment and existing
workspace policy preservation. Existing portal browser-unit checks pass. These
checks do not establish live Stripe account readiness or paid pilot completion.

## Runtime configuration and activation

The hosting portal, billing worker, billing-plan and hosting-limit commands,
fleet worker, Node build/deployment workers and static publication worker support
explicit test/live selection. Provider secrets must match that selection. The
portal rejects mixed legacy test flags and live settings. Its billing screen
uses the selected mode; merchant sales still display test-payment disclosures.

For a coordinated live activation:

- Portal: `--billing-mode=live`, `--billing-webhook-secret-file` and
  `--billing-management-config`. Both private files are required for live startup.
  Remove the legacy `--test-billing`, `--test-webhook-secret-file` and
  `--test-billing-management-config` flags. Existing test configurations still work.
- Hosting billing worker: add `"mode":"live"` to its private JSON alongside a
  live secret key, live plan prices and the fixed HTTPS return URLs.
- Fleet worker: set `"billing_mode":"live"` in its private JSON.
- Node build/deployment, publication and local Node build commands: set
  `--billing-mode=live` so entitlement checks use the same payment evidence.
- Plan/limit commands: set `--billing-mode=live`; create the live plan mapping
  and limits explicitly. Test plan rows are not promoted to live rows.
- Hosting policy: use `--billing-mode=live --require-subscription=true` for
  workspaces that require payment. Existing exemptions remain exemptions;
  selecting live mode does not enroll or charge customers automatically. New
  workspaces registered through a live-mode portal require a paid hosting
  subscription by default. Existing workspaces retain their saved policies.
- Stripe hosting webhook: configure the live signing secret for
  `https://portal.0xivanov.dev/webhooks/stripe-live`. The existing test route is
  `/webhooks/stripe-test`. A process accepts only its selected route and mode.

All participating processes must be restarted with matching mode configuration.
The database can retain both modes; it does not coordinate configuration across
processes. Do not activate just the portal while workers still use test mode.
Plan an explicit transition for existing sandbox subscribers, who do not have
live payment entitlement. Existing websites keep running when changes are held.

## Required next implementation

1. Merchant live provider constructors, signed event verification and schema 56
   persistence separation are implemented locally. Accounts, products, orders,
   events, refunds, buyer sessions and recovery records are isolated by mode.
   Existing records migrate to test mode. Runtime activation and truthful
   live merchant UI still need wiring; public configuration remains test-only.
   Hosting and merchant payment scopes remain distinct. Production remains
   schema 55 until an explicit rollout with a matching database/binary backup.
2. Activate provider configuration only after the owner has live account access,
   agreed prices/limits and customer-facing business and service details. Verify
   webhook routing and recovery without charging a customer. Any real purchase
   or charge still requires its own authorization.

## Other commercial gap

Domain code currently validates quotes and stores requests awaiting payment.
A production registrar adapter, registration/renewal fulfillment, ownership/contact
handling and payment-failure recovery remain implementation work. A registrar
account alone will not complete domain resale. Existing-domain connection is a
separate shipped feature and must continue to work during this work.
