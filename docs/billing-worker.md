# Stripe test billing worker

The worker processes persisted owner-authorized customer and checkout requests, verified checkout inbox events and periodic subscription observations. It uses Stripe test keys only. It does not enable paid hosting access or merchant sales.

Build `./cmd/billing-worker` and run with `--database /private/portal.sqlite --config /private/billing-test.json`. Both portal and worker must support the database schema. Back up an existing portal database consistently before upgrading; schema 14 needs a matching binary or restoration of the pre-upgrade backup for rollback.

The configuration must be a regular private file, with no group/other permissions, at most 16 KiB. Example values are placeholders:

```json
{
  "secret_key": "sk_test_REPLACE_LOCALLY",
  "success_url": "https://portal.example.test/billing/success",
  "cancel_url": "https://portal.example.test/billing/cancel",
  "plans": {"starter": "price_REPLACE"}
}
```

Return URLs must use HTTPS on the same host. The secret must be supplied locally, never in chat, source control or command arguments. The configured plans must match the operator's enabled database plan records. The worker intentionally does not create, re-enable or reprice plans on startup. Start the portal with `--test-billing` to opt into the owner request API. The portal includes an owner-only test billing screen; live payment use remains unqualified. Operator plan configuration is described below. The webhook is separately opt-in as described below.

Each task has a durable 60-second lease. Operations have a 30-second context deadline; canceled work recovers after lease expiry. Failed tasks back off from 30 seconds to one hour. The same customer/checkout request key is reused after uncertain results, and existing guards stop blind provider creates after 23 hours. Those aged requests need operator reconciliation. Completed creates and checkout events stop polling; subscriptions refresh every five minutes. Unsupported inbox event types remain for their future processors. Failed subscription reads keep the previous timestamped observation, which must never be assumed fresh by an access policy.

Diagnostics omit provider error bodies and customer details. A durable operator-facing failure view, alerts and task retention remain to be implemented. No live credentials, public webhook, real charge or deployment is configured by this command's addition.

## Owner request API

With `--test-billing`, authenticated workspace owners can use:

- `POST /api/billing/customer` with `workspace` to request or reuse the workspace billing identity; `GET` with the workspace query parameter reads its state.
- `POST /api/billing/checkout` with `workspace` and a configured `plan` to request or reuse a checkout; `GET` with `workspace` and `id` reads the pending/open/completed state and the acknowledged checkout URL.
- `GET /api/billing/subscription` with `workspace` and `id` to read the test subscription observation. This is not an access entitlement.

POST requests require the same-origin header and session CSRF token. The API accepts no price, provider acknowledgement or redirect URL. Billing requests do not contact Stripe within the browser request; the separately configured worker handles them. Endpoints remain disabled unless explicitly enabled.

## Test webhook endpoint

In HTTPS mode, add `--test-billing --test-webhook-secret-file /private/stripe-test-webhook-secret` to the portal command. The secret file contains the test endpoint signing secret beginning with `whsec_`, and uses the same private-file permissions and 16 KiB limit as other settings. The webhook cannot be enabled in plaintext demo mode. Without the secret configuration its route returns 404.

The exact destination is `/webhooks/stripe-test` on the configured portal origin. No query string, encoded path alias or browser Origin header is accepted. Register only a test-mode platform endpoint matching the pinned Stripe SDK API version; Connect events and live events are rejected. The currently processed event types are `checkout.session.completed`, `checkout.session.expired`, `customer.subscription.created`, `customer.subscription.updated` and `customer.subscription.deleted`. Subscription events trigger a fresh provider read rather than applying their embedded status. Other verified types remain pending until their processors are implemented.

Receipt requires a valid raw-body signature and is acknowledged only after durable inbox storage or duplicate recognition. Receipt itself grants no hosting access. The separately running billing worker resolves the acknowledged checkout identity and retrieves subscription observations. Browser billing routes continue to require session authentication and CSRF protection. This configuration has been tested with local synthetic signed events, not an actual Stripe endpoint.

## Configure a hosting plan

Build `./cmd/billing-plan`. Against a private, existing customer portal database, use:

```sh
billing-plan --database /private/portal.sqlite --plan starter --price price_REPLACE --enabled true
```

Use `--enabled false` to stop new requests for that plan. The enabled value is mandatory. The command refuses a missing database path so a typo cannot create a separate database. The configured Stripe Price must come from the test account and match the worker configuration; this local command does not verify or create provider prices.

Existing checkout intents keep their saved price. A pending intent whose plan was disabled or repriced cannot create a provider checkout until reconciled. Already-open provider checkouts are not canceled by this command. Existing subscriptions are not repriced or canceled.

`GET /api/billing/plans?workspace=...` lists enabled plan identifiers to workspace owners when test billing is enabled. It excludes Stripe Price IDs and does not quote amounts. Accurate price display from provider data remains required for the customer payment screen.

## Verified price refresh

The worker also refreshes enabled plan prices. A successful lookup schedules another after five minutes; failed or interrupted lookups retry after a 60-second reservation. Changes made through billing-plan clear previous price details and fence off earlier responses. Reapplying unchanged configuration preserves cached data.

Owners can request `/api/billing/offers?workspace=...` for enabled plans and their verified base prices. An unavailable or at least fifteen-minute-old observation yields a null price. Prices are not final tax quotes, and currency minor units must be formatted correctly by the customer screen. The endpoint does not contact Stripe during the browser request.

## Customer billing screen

With test billing enabled, workspace owners see Hosting billing beneath their projects. Set up test billing, refresh until the account is ready, choose a verified plan and refresh until the checkout link is available. The link opens Stripe in another tab. Use /billing/success and /billing/cancel on the configured portal origin as return URLs. The portal always retrieves saved status; a return URL does not confirm payment. Checkout completion awaits billing reconciliation. Enrolled sandbox workspaces use the separate hosting access policy below; checkout completion alone never enables hosting.

Subscription refresh signals require an already-bound subscription and matching customer. Events arriving before checkout identity binding remain pending. A valid signal clears the old observation, fences in-flight reads and reschedules the subscription task. Duplicate processing does not clear a newer observation. Refund and dispute event handling still require implementation before paid-access decisions.

Invoice refresh signals now include invoice.paid, invoice.payment_failed, invoice.payment_action_required, invoice.voided, invoice.marked_uncollectible and invoice.finalized. The invoice must explicitly reference an already-bound hosting subscription and its matching customer. Standalone invoices, unmatched subscriptions and conflicting customer/account identities remain unresolved rather than being assigned by metadata. Event amounts and status do not directly grant paid access.

## Stripe test subscription management

The optional portal flag `--test-billing-management-config /private/management.json` enables an owner-only management button. It requires HTTPS and --test-billing. The private JSON contains the worker fields secret_key, success_url, cancel_url and plans, plus configuration set to an operator-selected bpc_ test portal configuration. success_url must equal the portal origin followed by /billing/success. The secret stays in the private file; it is never a command argument.

Configure that Stripe test portal to allow payment-method updates, invoices and cancellation at period end. Keep plan and quantity changes disabled until upgrade handling is implemented. The integration retrieves the pinned configuration before session creation and checks the expanded configuration again in the response. It requires active test mode, no connected application, disabled public login, disabled subscription updates, enabled invoice/payment-method access and cancellation at period end without prorations. Keep the configuration under operator control and qualify it in sandbox before use.

POST /api/billing/manage accepts only workspace and requires an owner session, origin and CSRF token. The server resolves the saved customer, requests a short-lived management session, rechecks ownership and returns only a validated billing.stripe.com URL. It does not store that URL. Owner revocation after a URL was already issued cannot revoke the external session; Stripe controls its expiry. Cancellation or payment-method changes are completed in Stripe, and resulting events feed reconciliation. Returning to the portal never grants hosting access.

Reference: https://docs.stripe.com/customer-management and https://docs.stripe.com/customer-management/configure-portal. No real sandbox configuration or cancellation has yet been qualified.

## Refund and dispute signals

Supported risk events are charge.refunded, charge.dispute.created, charge.dispute.updated, charge.dispute.closed, charge.dispute.funds_withdrawn and charge.dispute.funds_reinstated. Current charge state is fetched and mapped through its invoice before consuming the event and refreshing the subscription. A failed or ambiguous lookup stays pending for retry/reconciliation. Refund creation requests and access policy remain unfinished. The disputed flag alone must not be treated as proof of an unresolved or lost dispute.

Charge observations now include verified dispute outcomes and DisputesChecked. Older snapshots without that marker require refresh before interpreting missing dispute data. Unknown or incomplete dispute results prevent a successful observation. Hosting access policy must examine current outcomes rather than using the charge's disputed boolean as a loss decision.

Known charges refresh every five minutes, using durable sixty-second reservation/backoff after failures. charge.succeeded now seeds charge tracking through the same verified-event path. A missing event for an entirely unknown charge cannot be recovered by this scheduler; a provider discovery sweep remains required.

Subscription reconciliation now discovers charges associated with its latest invoice and queues their verification even when the charge webhook was missed. Discovery requires exact customer/subscription identity and a supported single payment allocation. An empty result does not prove paid access. Historical invoices and unknown subscriptions still require a broader discovery sweep.

## Sandbox hosting access policy

`cmd/hosting-policy` opts a specific workspace into subscription enforcement:

```sh
hosting-policy --database /private/portal.sqlite --workspace WORKSPACE_ID --require-test-subscription true
```

Use this only for a sandbox workspace with test billing, plans, and the billing
worker configured. The command requires an existing database and an explicit
true/false value. It records the operator change. Passing false restores the
workspace's previous unrestricted hosting behavior. Schema 36 leaves existing
workspaces unrestricted unless explicitly enrolled; no live workspace is enrolled
by deployment or migration alone.

For an enrolled workspace, new projects, ZIP uploads, static publication, Node
builds and Node deployment requests require fresh saved subscription and payment
evidence. Worker claims and Node dispatch recheck the gate. Retrying a saved
request retains its original job; reads, cleanup and reconciliation remain
available. Running websites are not suspended or deleted by this policy.

A qualifying subscription must be active, within its billing period, unpaused,
and linked to the saved checkout and customer. Its latest invoice must be paid
with nothing remaining. Exactly one corresponding charge must have no refund,
no unresolved dispute and a completed dispute check. Subscription and charge
observations must be less than 15 minutes old and cannot be future-dated. A
scheduled end-of-period cancellation remains valid until the paid period ends.
Trialing subscriptions, zero-charge invoices, split payments and partial refunds
do not qualify under this initial policy.

The owner billing screen shows whether hosting changes are enabled or on hold.
`GET /api/billing/access?workspace=...` returns only the mode and allow/deny decision
to authenticated workspace members, with test billing enabled. Payment gating
returns HTTP 402; provider and payment identifiers remain private.

This is a sandbox policy, not authorization to accept test-mode payments for live
hosting. Production enrollment, plan-specific resource limits, grace/suspension
rules and recovery behavior under real provider events still need qualification.
Already-started work can complete and reconcile; this gate is not a runtime kill
switch. Billing observation freshness depends on keeping the worker running.

## Sandbox plan resource limits

Configure a known billing plan with `cmd/hosting-plan-limits`:

```sh
hosting-plan-limits --database /private/portal.sqlite --plan starter --projects 3 --uploads 10 --upload-mib 50 --node false
```

Every value is mandatory. Supported bounds are 1–100 projects, 1–20 retained source
ZIPs, and 1–100 MiB of compressed source ZIP storage. Node is an explicit true or
false. Limits cannot exceed the existing upload platform ceiling. These limits
apply only to workspaces enrolled in test subscription enforcement. Unconfigured
plans retain the existing platform defaults, including Node support. Legacy
workspaces remain unchanged. Schema 37 creates no plan limit records by default.

The plan comes from verified saved subscription evidence, not browser input.
A checkout saves the configured allowances when its request is created. Later
operator changes apply to future checkouts and do not rewrite saved allowances. If several subscriptions qualify, the
first by subscription ID supplies the plan, consistently for status and writes.
Plan-specific allowances are not summed. Production upgrade/downgrade terms and
multiple-subscription selection still require a customer-facing policy.

Project creation checks the workspace project count. Upload storage includes all
saved source versions across all workspace projects. Counts and byte checks share
the insert transaction. Deleting an unused upload frees its allowance. Lowering
plan limits affects future checkouts, while existing checkouts keep their saved limits.
Node exclusion blocks new Node project/uploads/build/deployment work, including
worker claims and dispatch checks; it does not stop a running Node site. Saved
request replay and cleanup remain available.

The offer catalog shows current plan limits before checkout. Billing status shows
usage and configured allowances, including whether Node.js is included. Source
ZIP storage is not total hosting storage: release artifacts, runtime disk, logs,
CPU, memory, bandwidth and build concurrency remain under separate platform caps
and are not yet billed or metered per plan.

## Saved checkout allowances

Schema 38 stores the allowance snapshot alongside the checkout's saved price.
Same-plan request retries, worker reads, subscription access decisions and the
billing screen all use that snapshot. The enabled plan catalog continues to show
current settings for a future checkout. Starting a new checkout captures those
settings in the same transaction as its immutable request. There is no browser
allowance override or endpoint for changing saved checkout allowances.

An unconfigured plan stores SQL NULL, preserving the platform default allowance
for that checkout even if custom limits are added later. Invalid saved JSON blocks
access rather than reverting to defaults. During upgrade, existing checkouts are
backfilled once from the effective plan settings to preserve pre-upgrade behavior.
That migrated snapshot stays fixed on subsequent restarts and plan edits.

Changing a paid customer's plan or renewing into different allowances still needs
an explicit upgrade/downgrade workflow. This change preserves hosting allowances;
it does not freeze every platform safety limit, complete pricing consent/versioning
between catalog display and checkout creation, or qualify live billing.
