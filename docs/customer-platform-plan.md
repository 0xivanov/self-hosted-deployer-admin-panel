# Customer accounts, project hosting, payments, and domain resale

Date: 2026-09-09. Status: proposed implementation plan. No production changes, purchases, or charges are authorized by this document alone.

## Active project goal

Deliver these four capabilities as one customer hosting MVP:

1. Domain purchasing and resale through the panel.
2. Multi-user login, workspaces and permissions.
3. Hosting payments and customer website sales.
4. Upload and deploy HTML/CSS/JavaScript sites and Node.js single-page applications.

Preserve the existing VPS/Pi deployments. A visual website builder is excluded. The goal is complete only after implementation, tenant-isolation checks, recovery tests and pilot qualification. Actual purchases require provider setup and transaction authorization; a development goal is not blanket permission to spend money.

## Agreed product scope

Customers register, create a workspace, upload an **HTML/static project or Node.js project**, configure it, and publish it with HTTPS. They can redeploy a new version, inspect build/runtime logs, restore a prior release, connect a domain, manage collaborators, and pay for hosting.

Two payment flows are included:

1. Customers pay the platform for hosting subscriptions.
2. Visitors pay customers for products/services sold through their hosted sites.

There is **no drag-and-drop editor or template builder** in this plan. WordPress/PHP hosting is also outside the first release. Initial delivery uses ZIP uploads; Git integration follows once the upload/build pipeline is qualified.

## Current code and gaps

| Component | Reuse | Missing |
| --- | --- | --- |
| Admin-panel Go service and frontend | UI conventions, operational views | Users, sessions, workspace ownership, upload/build flows, durable jobs and billing |
| CLI-backed panel | Existing operator app/node controls | Uses an environment-wide administrator credential; cannot be exposed to customers |
| Deployer server and agents | Deployment lifecycle, preflight, readiness, logs, domains, hosting profiles | User authorization, source builds, central customer/environment mapping |
| Named CLI environments | Separate customer runtimes | Portal-side orchestration and provisioning queue |
| B2 backups and monitoring | Existing operational patterns | Portal DB, source artifacts, immutable releases, billing reconciliation and restore coverage |

Evidence: admin-panel `internal/adminui/server.go`, `internal/client/client.go`, `cmd/admin-panel/main.go`; deployer `internal/server/auth.go`, `internal/server/app.go`, `internal/ingress/`, and `docs/hosting-poc-plan.md`. Existing authentication distinguishes administrators and agents. The earlier hosting POC explicitly excluded public signup, customer RBAC, shared-cluster tenancy and automated billing.

This is a new customer product built on the operational POC, not a small login-form extension.

## Proposed architecture

Keep the existing operator console at `admin.0xivanov.dev`. Add a separately runnable customer portal in the admin-panel repository, provisionally `app.0xivanov.dev`. It must run under a separate service identity with no access to the operator CLI configuration or Kubernetes credentials. Keep deployer runtime changes in the deployer repository and reuse its reconciliation engine.

- **Portal API:** users, workspaces, projects, uploads, environment settings, domains, deployments and billing. Every operation checks membership and ownership server-side.
- **Portal database:** separate SQLite database for the first single-instance pilot, with transactions, foreign keys, additive migrations, unique constraints and a persistent job/outbox queue. Move to PostgreSQL before scaling to concurrent portal replicas if needed. Do not retrofit these tables into the live deployer DB.
- **Private artifact storage:** separate B2 bucket and credentials for source archives, build artifacts and retained releases. Do not reuse the offsite backup bucket. Upload URLs are short-lived and bound to an authorized workspace, object key and size limit.
- **Build executor:** disposable isolated build VM per job, or a qualified equivalent VM boundary. Neither `npm ci`, install hooks, nor user build commands run on the Mac, portal host, control-plane VPS, or either Pi in production. Builders have no infrastructure or payment credentials; artifact access is short-lived and restricted to that job. Block metadata, private networks, control-plane addresses and unrelated storage; limit public package-download access, time, CPU, memory, disk, process count and output.
- **Runtime publisher:** resolves approved workspace/environment mappings and translates project configuration into deployment requests. Users supply no arbitrary server URL, CLI arguments, Kubernetes manifests, or runtime credentials. A small privileged worker owns environment credentials separately from the public portal. Validate each job again and bind it to the expected server identity.
- **Customer runtime:** initially one separate VM/environment per Node.js workspace. Customer code is untrusted; ordinary containers on a shared legacy host are not the isolation boundary. Do not join customer runtimes to the legacy WireGuard network. Dedicated environments cost more, so Node.js pricing must cover them. Shared untrusted compute is a later project requiring a qualified sandbox/microVM design.
- **Static runtime:** static files may share a dedicated platform-owned serving tier because they are not executed server-side. It gets read-only artifact access, no portal cookies or administrator secrets. Use a separate registrable content domain for customer sites and previews. Do not serve arbitrary customer HTML under the admin origin.

Operator node controls remain operator-only. Customers see their projects and assigned resources, never the fleet. Future direct customer deployer RPC access requires scoped identities and server-side resource authorization before release; UI hiding and app-name prefixes are insufficient.

## Supported upload contract

| Project | Initial support |
| --- | --- |
| HTML/CSS/JS | ZIP containing a configured document root and `index.html`; immutable static publishing; optional SPA fallback |
| Frontend source project | Node build with a configured static output directory such as `dist`; use the isolated builder, then static serving |
| Node.js HTTP server | `package.json`, npm lockfile, supported LTS runtime, optional build command, start command, health path, and `PORT` binding on `0.0.0.0` |

Select and pin a supported Node LTS version during implementation, document the supported matrix and patch policy, and reject unsupported versions. The initial package manager is npm with a lockfile; pnpm/yarn can follow. SSR frameworks work only when they meet the documented server contract. Do not promise universal framework autodetection. [Node's production release guidance](https://nodejs.org/en/about/previous-releases).

Uploads must reject path traversal, absolute paths, symlinks/hard links, duplicate normalized paths, archive bombs, excessive file counts, encrypted archives and oversized decompressed data. Ignore uploaded `node_modules`; reject `.git` and detected credential files such as `.env` with an actionable message. Secret detection is best-effort, not proof an archive contains no secrets. Do not expose original ZIPs publicly. [OWASP upload guidance](https://cheatsheetseries.owasp.org/cheatsheets/File_Upload_Cheat_Sheet.html).

Node.js MVP workloads are stateless. The filesystem is ephemeral; use approved external databases and object storage. Provide an environment-variable/secret UI with write-only secret values, encryption at rest and deployment-scoped injection. Runtime secrets are never available during builds. Build-time private credentials, persistent volumes, cron jobs, background workers, shell/SSH access and custom Dockerfiles are deferred. Document supported request timeouts, body limits and WebSocket behavior before launch.

## Accounts, roles and data model

| Role | Permissions |
| --- | --- |
| Platform administrator | Fleet, plans, support, environment assignment, suspension, audited platform actions |
| Workspace owner | Own projects, membership, secrets, domains, deployments and billing |
| Developer | Own-workspace project configuration, uploads, secrets, deploy and logs; no billing/membership changes |
| Viewer | Own-workspace non-secret project state and deployment status; no mutations |

Do not expose runtime/build logs to viewers by default because customer applications may log sensitive data. Decide any broader read-only role explicitly. Prevent removal of the last owner. Signup creates a normal workspace owner, never a platform administrator. Bootstrap the existing owner via a local administrative command and provide an SSH-only recovery procedure.

Tables: users, sessions, verification/reset tokens, workspaces, memberships, invitations, environments, projects, uploads, builds, releases, deployment_jobs, environment_secrets, domains, plans, subscriptions, billing_events, connected_accounts, products, orders, payment_events and audit_events. Use opaque IDs, tenant foreign keys, unique domain claims, revision numbers and idempotency constraints. Separate merchant sales records from hosting subscription records. Never store card data.

## Delivery phases

### P0. Contracts and compatibility, 2–3 engineering days

- Freeze upload/runtime contracts, ownership matrix, job states, supported Node version and deployment mapping.
- Record current API behavior and sanitized legacy manifests. Add two-customer fixtures and a disposable runtime test environment.
- Feature flags for customer accounts, public signup, upload/build, publishing, hosting billing and merchant payments. Preserve existing defaults.

**Exit:** concrete schema/API review, isolation boundary and rollback runbook. No live node or application changes.

### P1. Multi-user authentication and authorization, 6–10 days

- Signup/invitations, email verification, login/logout, forgot/reset password, profile and workspace membership.
- Argon2id password hashes with maintained libraries; bounded input and rate limits before expensive hashing. Opaque server-side sessions, Secure/HttpOnly host-only cookies, rotation, revocation, CSRF protection and generic recovery responses.
- Expiring single-use hashed verification/reset tokens; retryable transactional email outbox. Existing monitoring emails do not prove account-email delivery.
- Account disable, session revocation, audit records and platform-admin MFA/recovery. Customer MFA can follow the pilot.
- Permission checks on every resource, download, secret, log, background job and billing-portal request. Separate operator and customer routes and processes.
- Independent portal DB backup/restore. Keep existing operator login until owner bootstrap and recovery have been tested, then retire the shared browser credential without adding a public bypass.

**Exit:** Alice cannot read or mutate Bob's projects by altering IDs, upload keys, job IDs or API bodies. Password reset/logout/disable invalidate sessions; customer signup cannot create platform admins. [Password storage](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html), [sessions](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html).

### P2. Static upload and publication, 5–8 days

- Project wizard: HTML or Node type, upload ZIP, source validation, project settings and preview/status page.
- Implement HTML uploads first: isolated extraction, durable release artifacts, platform HTTPS URL and redeploy with a new ZIP.
- Domain ownership verification, unique claims, DNS instructions, certificate issuance/renewal and safe domain release. Customer content uses a separate domain and host-only portal cookies.
- Persistent jobs and immutable releases. Recheck authorization/quota before each state transition. Publish atomically; readiness failure keeps the prior live release. Restoring an old release uses its immutable artifact, not the current panel's in-memory snapshot.
- Bound storage, number of sites/files, upload rate, release retention and request volume. Tenant-specific asset resolution and preview access. Customer-side JS is allowed only on the isolated content origin.

**Exit:** customer uploads a static site, sees it live, redeploys, restores a prior version and connects a verified domain without YAML. Failed jobs and portal restarts do not break the existing site.

### P3. Node.js builds and dedicated runtimes, 10–16 days

- Add npm install/build/start settings, runtime choice, health path, environment secrets and build/runtime log views.
- Provision/assign a dedicated runtime environment through an operator-approved workflow first. Later automate provider purchasing separately; no automatic VPS purchases during implementation.
- Build in disposable VMs with no inherited tokens or private networking. Enforce time/resource/network quotas, artifact integrity and cleanup. Cover install hooks as untrusted execution, not just the configured build command.
- Produce an immutable runtime image; publish it using existing deployer preflight and deployment operations in the assigned environment. Use scoped registry credentials and keep manifests owned by the platform.
- Durable states: uploaded, validated, queued, building, deploying, ready, failed. Use job leases/idempotency and reconcile after worker crashes. A returned CLI response alone is not rollout readiness.
- Validate build/run architecture, health probes, shutdown, resource caps, proxy headers, WebSocket support if advertised, and secret redaction. No customer secrets in Docker layers or build logs.
- Restore prior release/config with explicit secret-version handling. Application/database rollback is separate; customer apps must keep data migrations backward compatible. No claim of automatic database rollback.

**Exit:** sample Express app and frontend build publish successfully; broken build and unhealthy deployment leave the prior site live. Malicious builds cannot contact private networks or access other customers' artifacts; a runaway app cannot affect another workspace or the legacy fleet.

### P4. Hosting subscriptions, 5–8 days

- Stripe Checkout + Billing + Customer Portal. Start with two monthly products: Static and Dedicated Node.js; define measured resource/storage/build limits and price the VM cost into the Node tier. Avoid an unlimited plan.
- Server owns price IDs and customer/workspace mappings. Dedupe Checkout creation. Only verified webhook/reconciliation state grants paid entitlement; a success redirect is not proof of payment.
- Verify raw-body signatures; persist before acknowledging, deduplicate event IDs, tolerate out-of-order events and reconcile missed events. Keep payment status separate from deployment status.
- Proposed policy for owner approval: 7-day renewal grace period; block new deploys afterward, suspend serving at the defined termination boundary, retain data for 30 days before a separate deletion workflow. Do not delete infrastructure/data on the first failed payment or a provider outage. Explicitly define external database retention and customer export.
- Test incomplete/declined payments, authentication-required flows, renewals, duplicate/out-of-order events, cancellation at period end, refund/dispute handling and recovery.
- Merchant activation, currency/prices, tax/invoice settings, privacy/cancellation/refund terms and support contact are prerequisites to live charges.

**Exit:** test subscription activates hosting; customer manages invoices/payment method/cancellation; other workspaces' billing is inaccessible. No real charges during tests.

### P5. Customer website sales, 8–14 days

Use Stripe Connect for each customer's merchant account, separate from their hosting subscription. Prefer hosted onboarding and direct charges, with the customer's business identified as seller. Validate supported merchant countries, fee responsibility, loss liability and business eligibility before selecting the final Connect account configuration. Do not assume a charge type alone resolves all liability. [Connect platform model](https://docs.stripe.com/connect/saas-platforms-and-marketplaces), [direct charges](https://docs.stripe.com/connect/direct-charges).

- Workspace owner chooses Enable payments, completes hosted onboarding, and sees verification/capability status. Block sales until required capabilities are active; handle later restrictions or disconnection.
- Initial commerce is one-time, fixed-price products/services with hosted checkout. Merchant creates a product/price and gets a Buy link usable on plain HTML. No private key is embedded in the website.
- For Node.js, provide a small documented API/example for creating checkout sessions using revocable credentials scoped to that workspace/site. Merchant account, allowed product/price and amount are resolved server-side; never trust a browser-supplied amount or account ID. Public Buy links do not expose order administration.
- Store merchant-scoped order/payment records. Confirm payment from verified Connect events, not the return URL. Do not send fulfillment twice on retries. Provide a signed, retryable merchant callback or order-status API for Node integrations.
- Order view and refunds for authorized owners, plus payment/refund status and references to the merchant dashboard. For initial static sites, fulfillment is manual and made explicit; paid digital downloads/shipping are not silently promised.
- Handle disputes, failed/pending payments, refunds and disabled accounts. Keep customer-sale refunds independent from the hosting subscription. Hosting suspension must not prevent access to existing order records, refunds or billing.
- Start without a platform percentage fee unless explicitly selected; hosting subscription revenue is sufficient for the pilot. Evaluate commission only after unit economics and merchant terms are chosen.
- Test two connected merchants: no cross-account products, orders, refunds or callbacks; duplicate events produce no double fulfillment; account restrictions stop new sales. Secrets and test/live modes are fully separated.

**Exit:** a visitor buys from a hosted HTML site and a hosted Node site in Stripe test mode, the correct merchant receives the sale, and refunds/onboarding restrictions work. A full cart, inventory, shipping, customer subscriptions, split payments and tax/fulfillment automation are later commerce features.

### P6. Paid pilot and release qualification, 6–10 days

- Three invite-only customers cover static hosting, Node hosting and commerce. Keep public signup closed until isolation and abuse controls pass.
- Run tenant-access, upload traversal/bomb, build escape/network, quota-race, secret exposure, domain takeover, billing retry and commerce-account isolation tests.
- Capacity/cost testing: concurrent builds, memory/disk exhaustion, traffic bursts and storage retention. Add abuse reporting, rate limits and administrative suspension. Verify cloud-provider rules allow the offering.
- Restore portal DB plus artifacts and secrets into a disposable environment. Reconcile payments without creating new charges or duplicate deployments.
- Verify migration/binary rollback, worker restart recovery, certificate renewal, backup freshness and webhook lag. Existing sites must serve independently of portal downtime.
- Promote only after recorded upgrade/rollback checks confirm no legacy app spec, node identity or data changes.

**Exit:** pilots complete signup, upload, publish, redeploy, hosting billing and merchant sales. Public release has an explicit go/no-go review and cost limits.

## Priorities and estimates

Core phases total **42–69 engineering days**, about **9–14 full-time engineering weeks**, plus provider activation, infrastructure setup and pilot feedback. This includes both payment flows and isolated Node hosting. These are initial planning estimates, not promises; refine after P0 and the build-isolation prototype.

Earliest useful milestone: accounts and HTML uploads (P0–P2, approximately 13–21 days). Hosting subscriptions can be developed after accounts/ownership are stable while Node isolation is built. Merchant integration can use synthetic sites before runtime launch, but public sales require P5/P6 gates. No editor development is required.

## Provider recommendation and cost controls

Choose Stripe first for both hosted subscriptions and Connect. Bulgaria is on Stripe's supported list, subject to the actual merchant's eligibility. Confirm the owner's business country instead of inferring it from timezone. [Availability](https://stripe.com/global), [hosted subscriptions](https://docs.stripe.com/billing/subscriptions/build-subscriptions), [webhook lifecycle](https://docs.stripe.com/billing/subscriptions/webhooks), [customer portal](https://docs.stripe.com/customer-management/integrate-customer-portal).

Paddle is not the proposed primary provider for this scope. Its policy targets software offerings and restricts platforms enabling other sellers to sell; any alternative requires approval for the exact business model. [Paddle restrictions](https://www.paddle.com/help/start/intro-to-paddle/what-am-i-not-allowed-to-sell-on-paddle). Implement one provider with a small adapter, not a general payment framework.

Keep pilot infrastructure small: one portal instance, private artifact storage, static serving tier and dedicated Node runtimes only for approved paid/pilot workspaces. Builders are disposable, capacity-limited and shut down after use. Measure VM-hours, storage, traffic, payment fees, email and support before fixing prices. Do not promise low shared-hosting prices while paying for dedicated VMs.

## Compatibility and release rules

- Existing VPS/Pis and apps remain the legacy environment. No public uploads, npm commands or customer builds run there.
- Customer portal and privileged publisher are separate processes and credential boundaries. Existing admin endpoints stay inaccessible to customers.
- Preserve CLI/agent defaults and protocol compatibility. New runtime capabilities are additive and capability-gated.
- Keep portal DB separate; snapshot before migrations and test the prior binary against supported schema transitions. Never restore stale billing data into live traffic without reconciliation.
- Deploy pilot-only routes behind flags. Rollback disables signup/build/deploy workers first; existing immutable sites continue serving.
- Never let a billing event or portal migration roll out or delete a legacy application. No data deletion is coupled directly to payment retries.
- Keep secrets out of Git and logs. B2 source-artifact credentials are separate from backup credentials. No customer receives an operator token or unrestricted Stripe key.

## Decisions and access needed later

No secrets or purchases are needed to start contracts and local auth/upload work. Before external Node execution, choose and fund a dedicated runtime/build provider and isolated network design. The Mac can test the portal and trusted fixtures; it is not a production sandbox for customer code.

Before public hosting: brand/content-domain choice, pilot users, resource limits, runtime budget, transactional-email sender, default Node version and database policy.

Before real payments: actual business country and Stripe/Connect activation, supported merchant countries and product types, plan currency/prices, fee policy, grace/retention/refund terms, and support contact. Use test credentials during development and private configuration for live credentials.

Immediate next implementation: P0, then a focused P1 change for the portal DB, owner bootstrap, sessions, workspace ownership and permission middleware. Add upload/static publishing only after two-workspace isolation is demonstrated.

## Added workstream: domain purchasing and resale

This is part of the active MVP goal, not just support for attaching an existing domain. Registrar selection and effort estimation remain open. The earlier 42–69 day estimate excludes this added workstream and must be revised after provider discovery.

### Registrar integration and ownership

- Evaluate registrar/reseller APIs for availability search, registration, sandbox access, supported TLDs, wholesale and renewal prices, deposits/minimums, renewals, transfers and customer registrant support. Confirm resale rights and terms before selection. DNS-record API access alone is not the registrar purchase integration.
- Use a provider adapter; implement one registrar first. Start with a limited standard-price TLD set. Exclude premium domains and complex eligibility requirements until explicitly supported.
- The customer is the registrant where the provider permits the resale model. Record registrant consent and provider-required contact data privately; document verification, privacy services and transfer-out rights. Operator purchases for a customer must explicitly identify that workspace.
- Workspace owners and authorized platform administrators can purchase/manage domains. Developers may configure approved DNS/site attachments but cannot spend money or transfer domains. Verify ownership on every registrar operation.

### Purchase and billing flow

- Search availability, obtain a short-lived server-side quote, show the full initial price, term, renewal price, fees/taxes and auto-renew choice, and require explicit purchase confirmation.
- Recheck availability and price before charging/registering. Use idempotent domain orders and reconcile unknown registrar/payment outcomes before retrying. Never charge or register twice after a timeout.
- Define a payment/registration state machine. Where supported, authorize payment before registration and capture on success. Otherwise document the compensation/refund flow. Failed registration must not silently consume a customer's payment; successful registration followed by failed capture requires an operator reconciliation case.
- Keep domain orders/renewals distinct from hosting subscriptions and merchant website sales. Hosting cancellation must not cancel or transfer a customer's domain.
- Link successful purchases to the workspace and provide DNS setup, site attachment and HTTPS status. Do not overwrite existing records without reviewing their purpose.

### Renewals and lifecycle

- Show expiry, renewal price, auto-renew state, transfer lock and verification status. Provide manual renewal and explicit auto-renew consent, with notifications before expiry and on payment/registrar failures.
- Schedule renewals ahead of expiry, reconcile provider state regularly, and alert operators about failures. Handle provider-specific grace/redemption periods without promising recovery outside provider rules.
- Support registrant verification and domain transfer-out with reauthentication, ownership checks and audit records. Account closure must resolve retained domains and pending renewals explicitly.
- Provide operator views for domain orders, renewal failures, refunds, registrar balance and margins. Alert before a reseller deposit is exhausted; funding the registrar account is a separate approved expenditure.

### Delivery order and acceptance

Implement registrar discovery alongside P0. Build purchasing after workspace authorization and platform payments are available, then connect it to website publishing. Include renewal/transfer handling before selling live domains. Use a provider sandbox or deterministic fake registrar for tests, then an explicitly approved low-cost pilot purchase.

Acceptance: a customer can buy and attach an available domain; unavailable/stale quotes cannot overcharge; duplicate callbacks/timeouts cannot duplicate purchases; failed registration is compensated; renewal and expiry notifications work; another workspace cannot access registrant details or modify/transfer the domain; hosting cancellation preserves domain ownership; transfer-out is possible. The four-item goal cannot be marked complete without this workstream.
