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
remain mandatory. No runtime caller has been switched to the live entry points.

Focused integration checks cover matching/mismatched keys and checkout responses,
signed event mode isolation, mixed-mode nested subscription objects, and the
existing provider suite. All network responses in these checks are synthetic;
no real customer, checkout or charge was created.

## Hosting persistence implemented locally

Schema 55 separates customers, plans, checkouts, subscriptions, charges, events,
worker tasks and plan limits by billing mode. Existing schema-54 records migrate
to test mode. Composite bindings prevent live records from referencing test
parents; reads, refreshes, deduplication, worker leases and hosting entitlements
use the selected mode. Existing sandbox evidence cannot authorize live hosting.
Authenticated provider clients declare their mode and mismatches are rejected
before provider calls or work leasing.

Runtime selection remains test-only: there is no public store mode setter or
operator flag wired yet. Production remains schema 54 with test billing.
This persistence change has not been deployed. A coordinated upgrade of all
portal database consumers and a consistent rollback snapshot are required before
schema 55 deployment. Old binaries must not run against the upgraded database.

Verification passed for billing, hosting and historical database migrations,
including same-ID event isolation, wrong-mode signed webhook rejection, sandbox
queue isolation and legacy payment history preservation. The provider integration
suite, Go vet for the affected packages and all command builds also passed.
Provider responses are fixtures; no live transaction was performed.

## Required next implementation

1. Bind the billing worker, portal checkout/management clients and webhook
   verification to one explicit operator-selected mode. Fail startup on mixed
   secrets or conflicting test/live flags. Recheck mode on nested event objects
   and reconciliation paths. Browser input must never select billing mode.
2. Make account billing screens describe the actual selected mode. Preserve
   test payment disclosures until the complete live path is configured. Existing
   sandbox subscriptions must never be represented as real paid subscriptions.
3. Apply the same separation to merchant Connect accounts, products, orders,
   refunds, disputes and buyer recovery. Hosting and merchant payment scopes
   remain distinct; completing one does not activate the other.
4. Activate provider configuration only after the owner has live account access,
   agreed prices/limits and customer-facing business and service details. Verify
   webhook routing and recovery without charging a customer. Any real purchase
   or charge still requires its own authorization.

## Other commercial gap

Domain code currently validates quotes and stores requests awaiting payment.
A production registrar adapter, registration/renewal fulfillment, ownership/contact
handling and payment-failure recovery remain implementation work. A registrar
account alone will not complete domain resale. Existing-domain connection is a
separate shipped feature and must continue to work during this work.
