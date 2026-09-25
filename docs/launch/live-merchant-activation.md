# Live website sales activation

Source implementation does not establish Stripe account readiness or activate
production. Production last recorded admin `417691c`, portal schema 55, test
payments. Live merchant activation is independent of hosting subscriptions.

## Configuration contract

The portal selects website-sales mode with `--merchant-mode=test|live` and reads
private Connect credentials from `--merchant-config`. Use
`--merchant-webhook-secret-file` for the signing secret. Live mode requires both
configuration and webhook secret, HTTPS, and a live Stripe secret key. The
legacy `--test-merchant-config` and `--test-merchant-webhook-secret-file` remain
sandbox-compatible; they must not be combined with live mode.

The merchant configuration JSON contains `secret_key` and supported `countries`.
The sales worker configuration additionally selects `mode`: omitted means test,
`live` selects live. Both processes must use matching merchant mode and the same
Stripe account. Hosting billing mode is a separate selection.

Configure the connected-account Stripe webhook at:

- Test: `https://portal.0xivanov.dev/webhooks/stripe-merchant-test`
- Live: `https://portal.0xivanov.dev/webhooks/stripe-merchant-live`

Only the selected route is active. Use the event types documented in
[merchant payments](../merchant-payments.md); do not reuse the hosting webhook
secret. A webhook signal prompts retrieval of canonical provider state before
fulfillment. Do not manually mark orders paid.

## Coordinated activation

1. Confirm the live Stripe platform account is activated and Connect is available
   for the intended merchant countries. Confirm business details, customer-facing
   support and policies. Obtain private live credentials and a separate live
   connected-account webhook signing secret without putting either in Git.
2. Stop portal database consumers, save a consistent database/configuration and
   binary backup, then deploy all consumers compatible with schema 56. Migration
   retains existing merchant history exclusively as test data. Restoring older
   binaries requires restoring their matching database, accounting for subsequent
   writes before any rollback.
3. Configure and restart the portal and merchant worker in the same merchant
   mode. A hosting-only worker need not select merchant mode; it does not process
   merchant records. Keep unrelated workers and existing websites running under
   their established configuration.
4. Confirm `/api/config` reports the selected merchant mode, the customer UI
   labels it accurately, and the opposite webhook route rejects delivery. Review
   worker results and account onboarding without charging a customer.
5. Onboard each merchant in live mode. Sandbox account bindings, catalog, orders,
   refunds and buyer recovery links are not promoted into live records. Existing
   hosting accounts and websites remain separate from merchant onboarding.
6. Verify a real authorized order, its webhook-driven status, recovery and any
   separately authorized refund before claiming live sales ready. Local tests
   and synthetic provider fixtures do not prove this step.

No outreach, purchases, charges or refunds are authorized by this runbook alone.
