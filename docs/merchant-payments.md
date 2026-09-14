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

## Sources checked 2026-09-14

- [Controller properties and responsibility configuration](https://docs.stripe.com/connect/migrate-to-controller-properties)
- [Hosted onboarding link creation](https://docs.stripe.com/api/account_links/create)
- [Connect testing](https://docs.stripe.com/connect/testing)

No provider account, customer charge or real merchant onboarding was performed.
