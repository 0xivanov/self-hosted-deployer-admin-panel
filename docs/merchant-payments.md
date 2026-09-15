# Merchant website payments

Merchant sales use a separate Stripe Connect account relationship from a
workspace's hosting subscription. A hosting Customer ID must never select the
merchant account for a website checkout or refund.

## Initial test adapter

`internal/merchantbilling` starts the provider integration with account creation,
account observation and hosted onboarding links. It uses the pinned Stripe Go SDK
and the Accounts v1 controller configuration documented by Stripe:

- Stripe collects merchant requirements.
- Merchants have the full Stripe Dashboard.
- The connected account pays its Stripe fees.
- Stripe handles connected-account negative balances from payments under this
  configuration. This does not remove responsibility for platform-account losses
  or establish eligibility and commercial terms for this platform.

This is an explicit test integration choice, not approval of a live commercial
configuration. Accounts v2 is also available for new Connect integrations; the
initial adapter uses the documented v1 controller API already supported by the
repository's SDK. Review the configuration during actual Connect platform setup
before creating production accounts. Do not silently switch controller properties
on an existing merchant relationship.

The operator chooses allowed merchant countries and fixed HTTPS return/refresh
URLs. Neither values nor account IDs may come directly from a browser. Account
creation requires a persisted 64-character request identity and returns only
account identity and capability flags, excluding personal/KYC fields. Returned
account country, metadata and controller configuration must match the request.
The account API does not provide a universal `livemode` field; the test-secret-only
client and separate test data are the mode boundary, not an invented response flag.

Onboarding links are single-use credentials. Provide them only to the currently
authorized owner through the authenticated panel; never email or log them. A link
request must use a separate durable request identity, fixed redirects and a
previously bound account. A return redirect is not a payment-readiness signal.
Recheck account capabilities and current permissions before customer checkout.

## Integration still required

1. Persist owner-authorized merchant intents and immutable workspace/account
   bindings separately from hosting billing. Enforce a unique provider account
   binding and recheck ownership before returning onboarding links.
2. Add durable account-creation and onboarding work. Persist the original payload
   before provider submission. Reuse an idempotency key only for its original
   request. An uncertain creation must be reconciled before another account is
   created, including after the provider's idempotency retention expires.
3. Add the owner panel, account-return/refresh routes and account-state refresh.
   Expired links require a new link intent, not a new merchant account.
4. Implement merchant-scoped products/orders, direct-charge hosted Checkout,
   verified Connect event processing and retryable fulfillment or order queries.
5. Implement owner-authorized refunds with durable request identities, reconciliation
   and merchant-scoped provider calls. Hosting refunds remain a separate flow.
6. Qualify Connect setup, supported countries, platform/merchant terms, test-account
   onboarding, payments, refunds and disputes in Stripe's sandbox. Obtain approval
   for any real charges and live account configuration.

## Durable account requests implemented

Schema 25 adds a merchant account table separate from hosting customers. An owner
can persist one immutable country/request per workspace. Repeating that request
returns its original identity; changing country does not create a second account.
Dispatch rechecks that the initiating user is still an enabled, verified owner,
then commits `submitted` before contacting the provider. Each intent can submit
account creation once. No automatic create retry follows a timeout or lost reply.

Successful provider responses bind a unique account to the exact request and
country. Unknown outcomes can be reconciled using a trusted provider lookup of a
candidate account, validating its ID, original metadata and fresh observation.
The caller must obtain that candidate from provider/operator evidence; arbitrary
browser-supplied account IDs must not reach this method. Binding does not require
the old actor to retain access after the external request, because recording its
result avoids losing a created account. All customer reads still require a
current owner, and private request/account/snapshot fields are excluded from JSON.

An interrupted request before provider submission may still remain `submitted`.
This deliberately requires operator investigation rather than assuming no account
was created. Bound reconciliation currently returns the saved mapping; periodic
capability observation is a separate remaining step. The store methods are not
yet exposed by the portal or a running merchant worker.

Schema 25 was tested only with disposable databases. Older binaries reject the
newer schema. A future rollout needs a consistent pre-upgrade database backup for
rollback; no live database was migrated.

## Sources checked 2026-09-14

- [Controller properties and responsibility configuration](https://docs.stripe.com/connect/migrate-to-controller-properties)
- [Hosted onboarding link creation](https://docs.stripe.com/api/account_links/create)
- [Connect testing](https://docs.stripe.com/connect/testing)

No provider account, customer charge or real merchant onboarding was performed.

## Owner panel and onboarding links implemented

The HTTPS customer portal accepts `--test-merchant-config /private/merchant.json`.
The private JSON contains exactly `secret_key` (a Stripe test secret) and
`countries` (an operator-approved array of uppercase country codes). Return and
refresh URLs are derived from the portal origin as `/merchant/return` and
`/merchant/refresh`. No user-supplied redirect is accepted. Keep this configuration
separate from hosting billing, and never check it into Git.

When enabled, owners see a Merchant setup panel. They can request their account,
continue a persisted but undispatched creation, open Stripe hosted onboarding,
and explicitly refresh account status. A submitted account with an unknown
outcome shows operator-reconciliation guidance. Page returns only load status;
they never create accounts or declare onboarding complete. The panel displays
capability flags, not a promise that website checkout has been implemented.

Onboarding records a fresh link request in the audit before provider access and
reauthorizes the session/owner and account binding after the provider responds.
The single-use URL is only returned to that authorized caller; it is not stored
or audited. Lost or expired links may be replaced with another link request for
the same bound account. Link and account-refresh requests share a durable
five-per-minute owner limit across workspaces.

Schema 26 adds a generation to merchant observations. Each refresh increments it
before provider access; a delayed earlier read cannot replace the newer result.
The refresh rechecks account identity and current owner permission. Existing
migration checks and focused merchant HTTP/recovery checks passed on disposable
databases. An older binary requires restoration of a consistent pre-upgrade
backup. No live database has been upgraded.

Still required: a trusted operator recovery command for uncertain creations,
automatic capability refresh/Connect webhook handling, real sandbox qualification,
merchant products/orders/checkout/fulfillment, refunds, and production rollout.

## Direct-charge checkout adapter implemented

`Client.CreateCheckout` and `RetrieveCheckout` operate in the previously bound
merchant's connected-account scope using the Stripe-Account header. They accept
an immutable `CheckoutOrder` from trusted server storage, not a browser-supplied
merchant account or final price. Creation uses `merchant-checkout-<request ID>`
as its idempotency key and writes the same order reference onto the Session and
PaymentIntent metadata. Retrieval uses the identical merchant scope.

The first test contract supports a single card-paid item in EUR, USD or GBP, with
an integer amount from 50 to 99,999,999 minor units. Prices are fixed and inclusive;
automatic tax, adaptive pricing, shipping and promotions are not enabled. This
does not calculate or establish a merchant's tax obligations. Production tax and
pricing policy needs qualification before sales launch. This adapter does not set
an application fee, transfer destination or hosting Customer ID.

Returned sessions must match the exact order, totals, currency and fixed portal
success/cancel routes. Live sessions, unsupported payment methods, added discounts,
shipping/tax adjustments and unsafe redirects are rejected. An open session needs
a Stripe-hosted checkout URL. Paid observations require a completed session and a
PaymentIntent reference; completed-but-unpaid sessions are not fulfillment proof.
The caller must verify, persist and reconcile orders independently of redirects.

This is currently the provider layer. Product/order persistence, price selection,
public checkout endpoints, return pages, merchant event handling and fulfillment
are still required. Do not expose it directly to website users before those
boundaries exist. Unknown checkout creation outcomes need reconciliation before
resubmission, particularly after Stripe's idempotency retention expires.

Local provider-fixture checks covered direct merchant scope on create/retrieve,
unchanged retry identities, exact amount/metadata/request fields, paid and expired
observations, foreign or mismatched responses, unsafe redirects and sanitized
errors. No actual Stripe checkout session or charge was created.

Sources rechecked 2026-09-14:

- [Direct charges with Stripe-hosted Checkout](https://docs.stripe.com/connect/direct-charges?platform=web&ui=stripe-hosted)
- [Create Checkout Session](https://docs.stripe.com/api/checkout/sessions/create)

## Owner product catalog implemented

Schema 27 stores products per workspace with name, currency, integer minor-unit
price, active state and revision. Current support matches test Checkout's
EUR/USD/GBP price range. The catalog permits up to 100 retained products per
workspace. Owners may create products before finishing merchant onboarding;
catalog availability alone does not enable payment acceptance.

`GET/POST /api/merchant/products` is available only with merchant configuration,
current owner authorization and the portal's normal HTTPS/session/CSRF checks.
Create request keys prevent repeated unchanged requests from creating duplicate
products. Edits require the exact revision read by the client; conflicts must be
refreshed rather than overwriting a newer price. Disabling a product preserves its
record. Order creation must later check that state and snapshot the selected
product revision and price before any provider request.

The panel supports creating, editing and disabling catalog entries with decimal
price input. It retains form values on errors and preserves other product edits
when a save succeeds. A lost create acknowledgement retains the original key;
changing that request requires checking the saved catalog first. Product loading
has independent workspace/request guards and is not reset by account-status
refresh. There is no Buy button or public sales endpoint yet.

Focused checks passed for exact prices, duplicate requests, stale edits,
workspace/developer denial, CSRF, validation, disabled feature behavior and the
catalog bound, together with existing migration checks. Synthetic UI checks
covered decimal parsing, retry identity, edit preservation and revision updates.
Only disposable databases were migrated. Use a consistent pre-upgrade backup for
rollback; older binaries reject schema 27.

## Durable orders

Schema 28 separates orders from mutable products. An order records its product
revision, name, amount/currency and merchant account before checkout submission.
The buyer supplies the revision shown on the purchase page; a stale revision
requires showing the current price again. Buyers never supply the final amount
or connected-account identity. Subsequent product price edits do not rewrite an
already agreed order. A disabled product blocks an undispatched checkout.

The order layer uses a hashed buyer token separate from portal owner sessions.
The future public purchase route must mint that unpredictable token securely and
keep it in a protected buyer cookie. Order IDs alone do not authorize buyer reads.
Owner order history uses current workspace-owner authorization. Private checkout
URLs, provider IDs and buyer/request identities are excluded from ordinary JSON.

Order requests require a currently bound, recently observed merchant account with
submitted details, enabled charges/payouts and active card payments. Dispatch
rechecks those conditions before committing the submitted state and making a
single provider creation attempt. Unknown creation outcomes remain submitted;
operator/provider evidence must identify the original session for reconciliation.
Never treat a missing reply as permission to create another payment session.

Reconciliation retrieves the exact session in the pinned merchant scope with the
original order payload. A durable observation generation fences delayed reads.
Mapped sessions cannot be replaced, paid status cannot regress to unpaid, and a
closed checkout cannot reopen through older evidence. Payment observations alone
do not provide a durable fulfillment mechanism, refund policy or delivery service.

Storage currently caps 10,000 orders per workspace and 20 new requests per buyer
per minute. Public-route abuse controls, order retention and complete paginated
owner reporting remain launch work. The store/coordinator is not a public purchase
endpoint; buyer-cookie handling, the purchase/consent page, return pages and
merchant event/fulfillment integration still need to be connected.

## Owner order history

Workspace owners can inspect the latest 100 website-sale orders in the merchant
panel and refresh the saved history independently of product editing. The
`GET /api/merchant/orders?workspace=<id>` endpoint requires a signed-in owner of
that workspace and enabled merchant configuration. Responses are non-cacheable
and omit buyer tokens/hashes, request keys, account/session/payment references
and checkout URLs. Developers and viewers cannot read the history.

The displayed order amount and product name come from the accepted order, so
later catalog edits do not change past purchases. Checkout state and payment
status are shown separately. Refresh reads saved observations; it does not contact
Stripe or prove fulfillment. The panel shows when an observation was last saved.
This history is separate from customers' hosting subscription payments.
