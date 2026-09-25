# Domain resale implementation

## First provider candidate

NameSilo is the initial candidate for a budget-conscious integration. Its reseller material describes free API access and its API reference documents a sandbox available by contacting support. Its reseller FAQ says there is no separate reseller price schedule; discounts depend on its discount program. These facts do not establish the cost or margin of a particular registration. Verify current registration and renewal prices, account funding requirements and applicable resale terms before enabling sales.

Sources checked on 2026-09-10:

- https://www.namesilo.com/reseller
- https://www.namesilo.com/support/v2/articles/domain-manager/reseller-frequently-asked
- https://www.namesilo.com/api-reference
- https://developer.openprovider.com/get-started.html

Openprovider is an alternative with a REST API and reseller tooling. No account, paid membership or registrar balance was created for either provider. NameSilo sandbox access and the exact sandbox endpoint/operation response contracts must be obtained and verified before implementing its transport. Its API puts credentials in the query string, so URLs must never appear in logs or surfaced errors; redirects and ambient HTTP proxies must be disabled. Do not use the production hostname as a test fallback.

## Initial purchase scope

The first domain product accepts ASCII second-level .com, .net and .org names, one year at a time. URLs, subdomains, IDNs, premium names and restricted/multi-label suffixes are excluded until their workflows are supported explicitly. Parsing normalizes case and surrounding whitespace without rewriting the requested label.

The provider must report availability, an explicit premium classification, registration cost, renewal cost and currency. A quote is usable for at most five minutes. Retail amounts use integer minor units and an operator-defined fixed markup. The quote is not a reservation or a final tax-inclusive charge. Availability and costs must be rechecked before purchase. Future renewal prices must be presented as estimates, with customer consent before charging.

The internal/domains package provides validation and quote arithmetic. The portal persists quotes and exposes owner-only quote routes and a form when a trusted reader is configured. Registrar transport and purchases are not implemented yet.

## Next implementation

1. Verify sandbox access and operation contracts for availability, normal/premium prices, registration, renewals, contacts, transfer locks and registration-status lookup.
2. Add a sandbox-only provider adapter, with bounded responses, private credentials, sanitized errors and explicit unknown-outcome handling.
3. Persist owner-authorized quotes and orders with the exact name, registrant contact, cost, markup, currency and consent. Separate domain charges from hosting subscriptions.
4. Recheck the quote, authorize payment and submit registration once. Reconcile timeout outcomes before retrying. Define compensation/refunds when registration fails.
5. Add customer domain inventory, expiry/renewal notices, explicit auto-renew consent, ownership/DNS linkage, cancellation/transfer lifecycle and operator reconciliation views.
6. Test duplicate delivery, concurrent buyers, expired quotes, price changes, registration uncertainty, payment failure and cross-workspace isolation, then obtain approval for a low-cost real purchase pilot.

### Saved quotes (local implementation)

Schema 15 adds workspace-scoped immutable domain quote snapshots. A trusted
`DomainQuoteReader` supplies normalized registrar evidence; operator configuration
supplies the fixed markup. The store authorizes an owner before fetching and again
before committing, validates freshness after the fetch, and stores provider evidence
privately alongside the retail offer. Reads expose only the retail offer and a
current expiry flag. Quotes do not reserve domains or authorize purchases.

Storage is currently bounded to 1,000 retained quotes per workspace and fails closed
at that limit, including before provider access. Automated archival/retention remains launch work. The quote HTTP API has
process-local request limits as described below. No registrar transport is configured. Future orders must reference a saved
quote and recheck current availability, price and owner authority before payment and
registration; an unexpired snapshot alone is insufficient.

### Customer quote API

`HTTPOptions.DomainQuotes` now enables authenticated `POST /api/domains/quote`
with exactly `workspace` and `domain` in the JSON body. `DomainMarkupMinor` is a
nonnegative operator setting, never accepted from the request. `GET` on the same
path takes `workspace` and `id` and returns the saved offer and current expiry.
The public configuration reports `domain_quotes`; both routes return 404 when no
reader is configured. No CLI registrar reader is installed yet.

Both routes use the existing host/TLS, session and workspace owner protections;
POST also requires origin and CSRF validation. Provider failures return a generic
503 without exposing provider errors. Each authenticated account has five lookup
attempts per minute across sessions and workspaces, separate from login limits.
The limiter is process-local with at most 4,096 account entries, so multi-instance
serving needs a shared limiter. Restarts reset these transient limits. Durable
quote storage remains capped independently. The earlier HTTP-rate-limit launch
item is now covered for a single portal process; retention is still outstanding.


### Customer quote form and provider contract research

The owner-only Find a domain form is controlled by the `domain_quotes` server
capability. It displays first-year retail cost, estimated renewal, tax uncertainty
and expiry, clears previous results on workspace changes/sign-out and ignores
responses from a previous workspace selection. Expiry updates while the page is
open; purchase-time checks must remain server-side. There is no purchase button.

Rechecked official NameSilo material on 2026-09-10:

- https://www.namesilo.com/api-reference/pages?uid=account/get-prices documents
  account-specific registration/renewal prices and optional retail/quantity inputs.
- https://www.namesilo.com/blog/en/building-a-domain-search-tool-with-a-registrar-api
  describes availability categories and the availability operation, but its sample
  is simplified and does not establish the explicit premium classification contract.
- https://www.namesilo.com/api-reference still directs developers to request sandbox
  credentials through support. A verified sandbox endpoint and actual response
  samples remain necessary. No support message or provider request was sent.

The form was exercised with a temporary loopback portal and synthetic registrar:
owner sign-in, EUR registration/renewal display, invalid-name rejection and sign-out.
The temporary server and test fixture were removed afterward. This is UI evidence,
not a qualification of NameSilo availability, pricing, tax or registration.

## Prepared domain orders

Owners can now prepare a domain order from a saved quote, inspect it and cancel
it. Preparation retrieves a new provider quote and requires unchanged registration
and renewal costs/currency, standard non-premium availability, and an unexpired
original retail offer. The owner is reauthorized after the provider read. Schema
33 stores the accepted offer and quote reference with state `awaiting_payment`.

An identical quote request replays the same order, including after cancellation.
Only one awaiting-payment order per workspace/domain is permitted; this does not
reserve the domain or prevent another workspace from expressing interest. There
is a 100-order workspace cap. Cancellation is owner-only and idempotent. New quotes
can be used after cancellation. Original owner identity is private.

The panel says payment setup is pending and the domain is not registered. No
customer payment, registrar funding or registration occurs in this step. Availability,
price, tax, payment authorization and registrant contact requirements must be
checked again before eventual purchase. Expired saved offers never authorize a
later charge. No real registrar reader or purchase transport is configured yet;
NameSilo sandbox endpoint/credentials remain required for that integration.


### Expired quote retention, September 25

Quote admission now removes unreferenced quotes only after their saved expiry
and creation time are both over 24 hours old. Cleanup is scoped to the authorized
workspace and shares the capacity transaction. Every order-linked quote is
retained, including canceled orders; malformed or missing expiry evidence is
retained for operator investigation. This prevents normal expired searches from
permanently exhausting the 1,000-quote workspace allowance. Order registration,
payment and renewal fulfillment remain unimplemented.

Focused verification covered capacity recovery, canceled-order evidence,
cross-workspace protection, recent history, malformed offers and the existing
quote/order flows. This is source-only; production domain quotes remain disabled.
The owner has offered existing Cloudflare DNS access for subdomains. Subdomain
provisioning uses an already-owned domain and does not replace domain resale.


### NameSilo account access verified, September 25

The owner supplied a local API credential. It is private (0600) and excluded
from Git through the local exclude file; no credential value is recorded here.
A read-only getPrices call through the VPS returned success code 300 and
registration-price fields for com/net/org. NameSilo reported the source IP as
159.195.146.26, confirming the configured IP restriction works.

This verified the production read API only. No domain was registered, renewed,
transferred or paid for, and no account funds were added. Sandbox credentials
and registration/renewal outcome contracts remain unverified. A usable
availability quote still requires explicit premium classification and fresh
availability, not just a TLD price list.


### Sandbox credentials received

The owner has now supplied a separately labeled sandbox key in the private,
Git-excluded local credential file. The supplied support instructions specify:

- Registry operations use OTE. Availability is not evidence of real-world
  availability; a sandbox registration consumes availability for sandbox users.
- Restrict initial qualification to .com and .net.
- Nameserver-change tests must use NS1.NAMESILO.COM through NS8.NAMESILO.COM.

The support reply excerpt began at item 2. The exact sandbox API base URL was
not included in the message or credential file; item 1 has been requested.
Do not send the sandbox key to the production endpoint or assume that an
environment label alone selects OTE. No sandbox request or registration has
been made at this checkpoint. Keep sandbox domain evidence separate from real
customer quotes and registrations.


### OTE endpoint and read contracts verified

The owner supplied the support-confirmed sandbox base URL:
https://ote.namesilo.com/api. Read-only requests from the VPS with the separate
sandbox key succeeded with response code 300 for getPrices and
checkRegisterAvailability. No production fallback was used.

For unique .com and .net sandbox names, checkRegisterAvailability returned an
available/domain element with attributes price, renew, premium="0", duration="1".
Both requested names appeared exactly once. This resolves the earlier missing
explicit premium-classification evidence for the observed standard-price
sandbox response. Monetary attributes are decimal strings; parse to integer
minor units without floating point. Reject absent, ambiguous or unsupported
premium/duration fields. Pricing must remain labeled sandbox; OTE availability
does not establish public registration availability.

The getPrices response included com/net registration entries, and the request
reported source IP 159.195.146.26. These calls did not register domains, renew,
change nameservers, add funds or charge anything. Registration and renewal
response/reconciliation contracts are still to be exercised in OTE. The quote
adapter, payment linkage and persistent domain lifecycle remain unfinished.
