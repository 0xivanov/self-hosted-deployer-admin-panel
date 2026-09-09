# Customer accounts, website publishing, and billing

Date: 2026-09-09. Status: proposed plan, not implemented. This document does not authorize production changes or purchases.

## Recommended first product

A customer signs up, verifies their email, creates a workspace, chooses a website template, edits text and images, previews it, purchases a hosting plan, and publishes. They can update the site, restore an earlier publication, attach a domain, invite a collaborator, and manage their subscription.

Start with an opinionated template/section editor for portfolios and small-business websites. Target three good templates and a small, accessible set of sections. A complete Wix-style canvas is a substantially larger product. One-click WordPress is another viable product but has a different runtime and maintenance burden.

Working assumptions, pending the owner's preferences:

- Hosting subscriptions are the first payment flow. Sales by customers through their websites are a later, separate payment flow.
- Invite-only pilot first, then public registration after isolation and abuse checks pass.
- Website content is data rendered by platform-owned templates. Customers cannot upload executable code, arbitrary containers, scripts, PHP, or arbitrary YAML in the first release.
- The existing operator console remains at admin.0xivanov.dev. A customer portal gets a separate origin, provisionally app.0xivanov.dev. Published sites and previews use a separate registrable domain, chosen before launch, rather than sharing the admin domain's cookie boundary.
- Existing applications and both Pi workers remain the legacy environment. This plan adds a product above the hosting POC; it does not claim that the POC already provides customer tenancy.

## What already exists and what must change

| Existing component | Reuse | Gap |
| --- | --- | --- |
| Go admin panel and static frontend | UI conventions, operational diagnostics | One Basic Auth login, no users, sessions, ownership, or durable portal DB |
| CLI-backed panel API | Keep for operators | Holds a global environment credential; not an authorization boundary for customers |
| Deployer server and agents | Managed application lifecycle, readiness, hosting profiles, domains | Admin/agent authentication, not customer resource permissions |
| Backblaze backups and monitoring | Operational patterns | New portal data, website revisions, payment records, and restore tests need coverage |
| Named CLI environments | Dedicated customer runtimes for later WordPress | Central website product and asynchronous provisioning are not implemented |

Repository evidence: admin-panel `internal/adminui/server.go`, `internal/client/client.go`, `cmd/admin-panel/main.go`; deployer `internal/server/auth.go`, `internal/server/app.go`, `internal/ingress/`, and `docs/hosting-poc-plan.md`. The original POC explicitly excluded customer RBAC, public dashboards, shared-cluster tenancy, and automated billing.

## Architecture and boundaries

Keep development in the admin-panel repository, adding independently runnable customer-portal and publisher services. Keep the deployer repository responsible for runtime deployment and any new narrow runtime capabilities. Do not duplicate its application reconciliation engine.

1. **Operator console:** platform administration and node management. Bootstrap the current owner as a platform administrator using a local command, never public signup. Migrate its browser authentication only after the new authentication path works; retain an SSH-only recovery procedure.
2. **Customer portal:** account, workspace, editor, domain, billing, and publication APIs. Runs under a separate service account, without the operator's CLI config, Kubernetes credentials, or node-management routes.
3. **Publisher worker:** consumes validated jobs from a durable queue. Receives tenant/site/revision IDs, not commands, paths, arbitrary image names, or YAML. Resolves ownership and entitlement again, generates an immutable static artifact, and atomically switches the published revision. Retry-safe and restart-safe.
4. **Website serving runtime:** platform-owned static serving software, read-only access to published artifacts, no portal cookies or control-plane credentials. Draft previews have unguessable expiring access URLs on the separate content domain and are not indexable.
5. **Storage:** separate portal SQLite database for the initial single-instance service, with transactions, foreign keys, migration checks, and a DB-backed job queue. Use a separate private B2 bucket for website assets and revisions; never reuse the backup bucket or its credentials. Public delivery goes through the serving runtime with tenant/site-scoped lookup. Restore tests must cover both DB and referenced artifacts. Move to PostgreSQL before adding concurrent portal replicas if measurements require it.
6. **Runtime placement:** first tests use local/disposable infrastructure. Before unrelated customers publish, provision a small separate website-serving VPS or another approved isolated serving environment. A shared server is acceptable here only for platform-generated static files without customer code execution. This is a new, restricted static-serving design, not permission to schedule arbitrary customer apps on the legacy cluster. WordPress uses separate customer VMs initially.

A namespace label or hidden UI button is not sufficient authorization. Customers must never obtain the current full administrator credential. If a future customer feature must call the deployer API directly, implement and test scoped identities and resource ownership in the deployer server before exposing that feature. Do not rely on an `app_name` prefix or customer-supplied tenant ID.

## Roles and ownership

| Role | Allowed actions |
| --- | --- |
| Platform administrator | Manage platform, nodes, plans, customer suspension and support audit |
| Workspace owner | Manage that workspace, members, sites, domains and billing |
| Editor | Edit, preview and publish that workspace's sites; no billing or membership changes |
| Viewer | Read workspace and site status; no edits, publication or billing changes |

Membership is explicit and checked server-side on every request, upload, artifact fetch, background job, billing portal session and domain operation. Signup always creates an ordinary owner of a new workspace, never a platform administrator. No customer can list infrastructure nodes or inspect another workspace's logs, files, identifiers, invoices, or configuration. Prevent removal of the final workspace owner. Support access must be explicit and audited; do not add silent impersonation for the MVP.

Suggested portal tables: users, sessions, verification_tokens, password_reset_tokens, workspaces, memberships, invitations, sites, site_revisions, assets, domains, publication_jobs, plans, subscriptions, billing_events, audit_events. Use opaque IDs, unique normalized emails and domain claims, workspace foreign keys, hashed single-use tokens, revision numbers, and uniqueness constraints for idempotency. Do not store payment-card details.

## Implementation phases and acceptance criteria

### Phase 0: contracts and compatibility baseline, 2–3 engineering days

- Record current API responses, auth behavior, deployments and migration baseline.
- Define roles, resource ownership, template schema, job state machine, publication pointer, plan limits and API contracts.
- Add feature flags: customer accounts, signup, publishing, billing, and WordPress. Defaults preserve the existing console.
- Establish synthetic two-customer fixtures and test payment data. No live customer or credential fixtures.

Exit: reviewed permission matrix and schema, tests for legacy paths, and a concrete deployment/rollback runbook.

### Phase 1: account system and authorization, 6–10 days

- Email/password signup, verification, login, logout, forgot/reset password, invitations and profile page. Invite-only mode initially.
- Use established cryptographic libraries and Argon2id password hashing. Use opaque server-side sessions in Secure, HttpOnly, host-only cookies with appropriate SameSite policy. Rotate sessions at authentication; revoke on password reset, account disable and membership changes. Add CSRF protection for all cookie-authenticated mutations.
- Persist single-use, expiring hashed verification/reset tokens. Generic account-recovery responses, bounded password lengths, login/reset throttling, and audit events without secrets. Rate-limit before expensive hashing.
- Add MFA for platform administrators before public launch; recovery codes are hashed and single use. Customer MFA can follow the pilot.
- Add workspace membership middleware and role-aware navigation. Never infer permissions solely from frontend state.
- Configure transactional email separately from operational alerts. Existing delivery of monitoring mail does not prove verification/reset email deliverability.
- Add independent portal backups and a tested admin recovery command. No shared Basic Auth credential as a public fallback.

Exit: two users cannot access each other's resources by changing IDs, URLs, API bodies or asset paths; logout/reset/revocation work; SMTP failure does not create an activated account or lose retryable mail.

### Phase 2: template editor and reliable publishing, 8–14 days

- Site wizard: name, template, theme, pages, logo, and platform URL.
- Three responsive templates with sections such as hero, services, gallery, about, and contact details. Edit text/images, add/remove/reorder approved sections, set colors/fonts, and preview mobile/desktop. Contact links first; form submission and spam handling are a separate feature.
- Autosaved drafts with optimistic concurrency, basic SEO title/description, favicon and social preview image. Preview does not change the live site.
- Validate uploaded file type, size and pixel dimensions; reject active content and sanitize rich text. Ignore arbitrary external asset-fetch instructions. No user build scripts or package installation.
- Publish jobs: queued, rendering, publishing, ready, failed. Record stable job IDs and timestamps. Retry without duplicate sites; check membership and subscription again before publication. Only mark ready after a public probe succeeds.
- Store immutable revision artifacts and a durable current-publication pointer. Failed jobs retain the prior working revision. Restore means selecting a retained site revision, not the current operator panel's process-local snapshot.
- Default platform address plus domain wizard with ownership verification, unique claims, DNS instructions, automatic HTTPS and renewal alerts. Removal must release ownership safely and prevent dangling-domain takeover.
- Enforce site, page, asset-storage, upload and publication-rate quotas. Explain limits in the UI and enforce them transactionally on the server.

Exit: a nontechnical pilot user publishes a small site without YAML, updates it, restores a prior version, and attaches a verified domain. Another workspace cannot read unpublished content or alter routing. Restart during publishing leaves a recoverable job and a working prior site.

### Phase 3: hosting subscriptions, 5–8 days

- Start with one paid monthly plan and explicit limits. Offer unpublished drafts before payment; optional pilot entitlement is an audited operator action. Final price needs measured hosting, backups, email, payment-fee and support costs, not an invented margin.
- Stripe-hosted Checkout for subscription purchase and Stripe Customer Portal for invoices, payment-method updates and cancellation. Server chooses allowed prices and workspace/customer mappings; never trust a browser-supplied amount or Stripe customer ID.
- Verify webhook signatures against the raw request body. Persist/deduplicate events before acknowledgment; handle retries and out-of-order delivery, and reconcile periodically with Stripe. Use idempotency keys for Checkout creation and provisioning. The browser success redirect does not grant access.
- Model billing status independently from deployment status and derive entitlements explicitly. Allow one active hosting subscription per workspace for the MVP.
- Proposed policy for approval: 7-day failed-payment grace period; notify owner and block new publications after grace. At cancellation period end, suspend public serving while preserving access to billing/export. Retain site data for 30 days before the separately scheduled deletion workflow. Provider outages or missed webhooks must not immediately delete sites.
- Test incomplete payments, authentication-required payments, renewals, declines, duplicates, out-of-order events, cancellation at period end, refund/dispute handling and recovery after missed events.
- Publish pricing, cancellation/refund terms, privacy and retention information before charging real customers. Merchant account activation, tax/invoice configuration and support contact are launch prerequisites, not solved by installing an SDK.

Exit: test-mode signup → Checkout → webhook → entitlement → publication works, and a customer can manage billing without seeing another workspace's account. No real charge is made during implementation tests.

### Phase 4: paid pilot qualification, 5–8 days

- Invite three pilot customers; keep public signup disabled until tenant-isolation and abuse checks pass.
- Test malicious content, unauthorized object access, upload traversal, preview isolation, quota races, domain ownership, job retries and billing state transitions.
- Validate the separate serving environment's capacity, costs, asset limits and recovery. Add basic abuse reporting, administrative suspension, and resource alerts. Avoid unlimited hosting plans.
- Restore the portal DB plus site assets to a disposable environment. Confirm subscriptions reconcile correctly without charging again or publishing stale content.
- Upgrade and roll back the portal with existing sites still serving. Monitor login errors, failed publications, queue backlog, certificate renewal, webhook lag and backup freshness.
- Remove the obsolete shared browser login once the owner account and SSH recovery are verified. Keep legacy deployer CLI and agent authentication unchanged.

Exit: all three pilots complete account creation, publishing, billing and recovery exercises; no changes to existing application specs, node identities or storage; recorded go/no-go review for opening signup.

Estimated core effort: **26–43 engineering days**, roughly **5–9 full-time engineering weeks**, plus external account/DNS/email setup and pilot feedback. These are planning ranges, not delivery promises. First demoable accounts and drafts come earlier; payment-enabled public launch requires all gates. Review the estimate after Phase 0.

## WordPress and a fuller visual builder

**WordPress option, roughly 8–15 additional engineering days for a qualified basic package:** one-click install, supported PHP and MySQL/MariaDB versions, persistent uploads, database credentials, application-specific offsite backup/restore, admin credential delivery, HTTPS, staged core/theme/plugin updates, monitoring and quotas. Use WordPress's existing editor. Do not equate the deployer's current PostgreSQL support with WordPress support. Start with separate customer VMs and a reviewed plugin policy; install success alone is not commercial readiness. See [WordPress requirements](https://wordpress.org/about/requirements/).

**Fuller Wix-like option, estimate after an editor prototype:** drag-and-drop layout canvas, nested/responsive layouts, undo/redo, reusable blocks, richer media library, collaboration and template compatibility. Evaluate embedding a maintained editor with an acceptable license before building one from scratch. Budget multiple additional weeks or months depending on scope. WordPress hosting and this editor are separate choices, not sequential prerequisites.

## Payment-provider recommendation

Use **Stripe Checkout + Billing + Customer Portal** first. Bulgaria is listed as a supported business location, subject to account onboarding and the actual merchant's eligibility. Its hosted integration covers the recurring hosting flow we need. Verify local fees and merchant details before setting retail prices. Sources: [availability](https://stripe.com/global), [subscriptions](https://docs.stripe.com/billing/subscriptions/build-subscriptions), [webhook lifecycle](https://docs.stripe.com/billing/subscriptions/webhooks), [customer portal](https://docs.stripe.com/customer-management/integrate-customer-portal).

Keep a small billing adapter around checkout, customer-portal links, normalized subscription events and reconciliation. Implement one provider first; do not build a general payment framework.

Paddle is worth evaluating only if its merchant-of-record offering is suitable for the exact hosting/software product and it accepts the business. Do not assume generic hosting or customer marketplace payments are approved: its published policy targets software businesses and restricts marketplaces. [Paddle product restrictions](https://www.paddle.com/help/start/intro-to-paddle/what-am-i-not-allowed-to-sell-on-paddle).

If customers also need to sell from their sites, the quickest separate feature is a link to each customer's own hosted payment page. Full integrated commerce needs products, orders, fulfillment, refunds and merchant onboarding. Evaluate Stripe Connect for that phase; never route all customers' sales through the operator's ordinary hosting subscription account. [Stripe Connect platform model](https://docs.stripe.com/connect/saas-platforms-and-marketplaces).

Authentication implementation references: [OWASP password storage](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html) and [session management](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html).

## Compatibility and release rules

- No customer gets access to the existing full-control API or the legacy three-node environment.
- Keep current CLI/agent contracts and default deployment behavior unchanged. New deployer capabilities are additive and capability-gated.
- Keep the new portal DB separate from the deployer DB. Snapshot it before migrations and prove compatible binary rollback; use maintenance recovery only if a schema cannot support the old binary.
- Deploy new services behind flags and pilot routes. Rollback disables signup/publication before replacing the portal; existing immutable sites continue serving independently.
- Do not let payment status, migrations, or a portal restart trigger legacy application deletion, data restoration, or rollout.
- Keep secrets outside Git and logs. Existing production credentials are not fixtures or customer credentials.

## Decisions and access needed

Before Phase 2: template editor versus WordPress priority; brand and content-domain choice; pilot customer group and serving-environment budget.

Before Phase 3: hosting-only payments versus commerce; merchant's actual business country and Stripe account; plan currency, prices, limits and approved grace/retention policy; transactional-email sender/provider. Supply credentials through private configuration, not chat.

No new VPS or live payment credentials are needed to write contracts, implement local account flows, or build the first simulated editor. They become dependencies for real external hosting and live billing, respectively.

Immediate next implementation: Phase 0, followed by a narrowly scoped PR for the portal database, owner bootstrap, sessions and permission middleware. Then add signup/invitations and verify two-workspace isolation before any customer publishing route.
