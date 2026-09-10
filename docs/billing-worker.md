# Stripe test billing worker

The worker processes persisted owner-authorized customer and checkout requests, verified checkout inbox events and periodic subscription observations. It uses Stripe test keys only. It does not enable paid hosting access or merchant sales.

Build `./cmd/billing-worker` and run with `--database /private/portal.sqlite --config /private/billing-test.json`. Both portal and worker must support the database schema. Back up an existing portal database consistently before upgrading; schema 11 needs a matching binary or restoration of the pre-upgrade backup for rollback.

The configuration must be a regular private file, with no group/other permissions, at most 16 KiB. Example values are placeholders:

```json
{
  "secret_key": "sk_test_REPLACE_LOCALLY",
  "success_url": "https://portal.example.test/billing/success",
  "cancel_url": "https://portal.example.test/billing/cancel",
  "plans": {"starter": "price_REPLACE"}
}
```

Return URLs must use HTTPS on the same host. The secret must be supplied locally, never in chat, source control or command arguments. The configured plans must match the operator's enabled database plan records. The worker intentionally does not create, re-enable or reprice plans on startup. Start the portal with `--test-billing` to opt into the owner request API. Plan administration, customer payment screens and the dedicated webhook endpoint still need wiring before customer use.

Each task has a durable 60-second lease. Operations have a 30-second context deadline; canceled work recovers after lease expiry. Failed tasks back off from 30 seconds to one hour. The same customer/checkout request key is reused after uncertain results, and existing guards stop blind provider creates after 23 hours. Those aged requests need operator reconciliation. Completed creates and checkout events stop polling; subscriptions refresh every five minutes. Unsupported inbox event types remain for their future processors. Failed subscription reads keep the previous timestamped observation, which must never be assumed fresh by an access policy.

Diagnostics omit provider error bodies and customer details. A durable operator-facing failure view, alerts and task retention remain to be implemented. No live credentials, public webhook, real charge or deployment is configured by this command's addition.

## Owner request API

With `--test-billing`, authenticated workspace owners can use:

- `POST /api/billing/customer` with `workspace` to request or reuse the workspace billing identity; `GET` with the workspace query parameter reads its state.
- `POST /api/billing/checkout` with `workspace` and a configured `plan` to request or reuse a checkout; `GET` with `workspace` and `id` reads the pending/open/completed state and the acknowledged checkout URL.
- `GET /api/billing/subscription` with `workspace` and `id` to read the test subscription observation. This is not an access entitlement.

POST requests require the same-origin header and session CSRF token. The API accepts no price, provider acknowledgement or redirect URL. Billing requests do not contact Stripe within the browser request; the separately configured worker handles them. Endpoints remain disabled unless explicitly enabled.
