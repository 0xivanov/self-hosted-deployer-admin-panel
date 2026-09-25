# Launchstead: freelancer-first launch plan

Updated September 25, 2026. This plan narrows the launch audience; it does not declare the original four-part hosting goal complete.

## Customer and promise

Primary buyer: independent web developers and small agencies managing a handful of client websites. Secondary: indie developers with small static or Node.js applications that fit the documented runtime constraints. Business owners receive the hosted website, often through their developer. We are not offering a website builder or complete business software suite.

Promise to validate: bring an existing website, publish it on a client domain, and manage updates without maintaining servers. Initial differentiation is assisted onboarding, understandable limits, and convenient client website management. These are hypotheses, not proven competitive advantages.

## Release sequence

### 1. A coherent client website workspace (started)
- Website-focused navigation, searchable portfolio and type filters.
- Light, dark and system appearance using shared visual tokens.
- Onboarding tied to actual ZIP, build, publish and domain steps.
- Explicit workspace-wide team access warning; do not imply project-level client isolation.
- Landing page and outreach drafts aligned to this audience, with private access and test billing stated honestly.
- Shipped: compact website portfolio with current workflow status and published hostname; dedicated in-place website management with publishing/domain/settings shortcuts, back navigation and support contact. Existing forms and polling remain attached during navigation.
- Next: richer last-deployment summaries and operational diagnostics. Capacity admission is shipped below. Preserve file inputs and ongoing operations during status refresh.

Acceptance: an invited developer finds a site, uploads a revision, publishes and connects a domain without operator explanation. Test empty, failed, waiting and live states in both themes.

### 2. Remove the launch blockers
- Shipped: transactional configured fleet-capacity admission using project rows as reservations, including deleting projects; workspace allowance and static/Node availability displayed before creation. Remaining: operational health-aware provisioning diagnostics and reconciliation for orphan assignments created outside this flow.
- Shipped: safe build/deployment stage explanations, explicit failed-publication retry, request timestamps/support references and long-wait guidance. Retained release restoration is available in history. Shipped: on-demand sanitized failed-build output for owners/developers. Shipped: on-demand runtime log snapshots through a private deployer bridge, with authorization rechecked after fetch. Remaining: richer build output and broader operational recovery. No invented progress percentages.
- Usage summary and straightforward support contact.
- Verify and document restoration and failure handling appropriate to a limited paid pilot. Existing private-launch shared-kernel infrastructure is not an unrestricted public execution service.

Acceptance: over-capacity requests fail clearly before a project is accepted; a bad update does not unnecessarily replace a healthy site; the customer can recover and the operator can diagnose issues.

### 3. Freelancer collaboration
- Shipped first slice: owner-managed, project-scoped read-only client review for existing verified accounts. A separate Shared with me view exposes only the granted website name/type, publication summary and active custom-domain address. It grants no workspace membership, source/upload access, DNS proof, logs, billing, publishing or editing. Add/remove actions are audited; revocation takes effect on the next request. Maximum 20 clients per website.
- Shipped: scoped email invitations with seven-day expiry, matching verified-account acceptance, owner authorization recheck, revocation, replacement, rate limits and encrypted queued delivery. New recipients must be approved under the existing signup policy before an invitation is sent; existing verified accounts can be invited while signup is closed. Invitation creation does not grant access.
- Shipped: owner-only recent client-access history with actor, recipient and date for direct grants/removals and invitation request/revocation/acceptance. It displays the latest 50 recorded changes and excludes unrelated audit data. Installed in production with admin `5e19bcf`.
- Implemented: workspace-only client labels, visible on portfolio cards and searchable alongside website names. Owners/developers can save or clear a label in Website settings without a reload; labels grant no client access. Live in production with admin `aca110e` and portal schema 54.
- Shipped: exact client-label and deployment-status filters plus alphabetical portfolio sorting. Filters preserve selected website details and existing forms.
- Remaining: ownership handover and broader portfolio summaries. Existing Team roles are still workspace-wide. Registration remains restricted to the operator-approved allowlist. See [client onboarding](client-onboarding.md).
- Ownership handover requires explicit membership and billing decisions; a client label does not transfer ownership.
- Implemented: reviewable, copyable website status snapshots with a timestamp and public address, excluding internal labels, logs, files and account details. No message is sent and no access is granted. Live in production with admin `e4f0bec`.

Acceptance: a client sees only the intended website; cross-project APIs deny access, including logs, uploads, domains and releases.

### 4. Developer deployment paths
- Registry images: Docker Hub/GHCR metadata resolution, ARM64 digest pins, project-scoped encrypted credentials/environment versions, immutable releases, customer controls and deployment queues are implemented. Core candidate generations, traffic activation/recovery, lost-response handling, retry, retirement and deletion are integrated with the fleet. The supporting core/CLI/portal/workers/controllers are installed in production (core schema 12, portal schema 54), with Docker/candidate flags still disabled. Real isolated cluster checks passed public-image publication across both Pi workers, trusted HTTPS, failed-update recovery, delayed-writer rejection, successful update after reopening state, retirement and deletion. Remaining activation work: private-image pull/credential rotation, shared credential configuration and actual portal publication. Unused saved credential/environment removal is implemented, with retained-release protection and prepare/delete race checks. Retiring saved releases and reclaiming their now-unneeded versions remains unfinished. See [registry deployment](registry-image-deployment.md) and [production upgrade](production-upgrade-20260925.md).
- GitHub connection and deploy-on-push: implementation started with bounded signed push validation, repository archive preparation using the existing static/Node upload rules, and GitHub App signing/repository-scoped read-only token access. Provider user/installation/repository access checks and hashed, session-bound single-use link requests, OAuth exchange and revisioned project connection persistence (local schema 53) are now implemented. Customer connection handlers, repository selection and disconnect controls are now implemented behind private GitHub App configuration. Manual imports now use a durable leased queue, pinned-commit downloads and the existing validated upload path. Guided repository access is implemented locally. Durable authenticated webhook intake, replay handling and a leased processor that imports only the current branch head are implemented locally. Automatic static publication and Node build/deployment orchestration, project controls and deployment activity are implemented locally. Branch-return replay handling is implemented locally. Bounded history cleanup is implemented locally. Real App installation and production activation remain unfinished; no webhook or GitHub deployment is enabled in production. See [GitHub deployment](github-deployment.md).
- Dockerfile builds later, with resource limits and isolation. Define storage/database support explicitly; do not market all MVP workloads as supported.

### 5. Paid private launch
- Hosting billing is still test-only end to end. Explicit live-mode provider support, mode-separated hosting persistence/entitlements, worker/HTTP configuration and UI mode handling are implemented. Admin `fa4eda4` is deployed with schema 56 and unchanged test hosting billing. Merchant live-mode support is installed but merchant sales remain disabled pending provider setup. Live provider/business configuration and verification remain before real-payment activation. See [live billing implementation](live-billing-activation.md). Activate only after business/provider setup, pricing and customer terms are ready.
- Account for compute, storage, backups, payment fees and support time when setting plans. Never invent prices in marketing before choosing limits and margins.
- Domain resale and merchant website sales stay on the original roadmap, separately gated by provider activation and lifecycle support.
- Recruit five real customers with assisted onboarding: two freelancers, two indie developers, one business with an existing site.

## Commercial work and measurement

Publish only verified features. No invented testimonials, uptime guarantees, unlimited resources or unimplemented integrations. Prepare outreach but do not send messages without authorization.

Measure invite-to-first-live-site time, onboarding completion, first successful update, support minutes per account, second website added, paid conversion and recurring gross margin. Collect only necessary operational data; introduce analytics deliberately with privacy documentation.

After the first five customers, prioritize observed blockers and retention over a broad feature checklist. A website that stays useful and affordable to support is a stronger milestone than feature count.

## Deployment and boundaries

Use the existing deployer and fleet. Keep the current website/data model while improving its interface. Do not migrate databases or modify routes for presentation work. Roll out with a previous binary available and smoke-check public endpoints. Do not delete temporary or custom domain connections until the pending ambiguity in the user's domain-removal request is resolved.

Public registration, actual charges, domain purchases, outreach sending and broader service promises require their respective readiness decisions. A plan or marketing draft is not authorization for those actions.

## Client review release, September 23

Schema 41 adds only `project_clients` and its user lookup index. The portal and all installed portal database consumer binaries must be upgraded together; schema-40 binaries refuse the new database. The fleet Python controllers use compatible queries and deletion already enables foreign keys, allowing grant cleanup to cascade.

Focused verification: owner-only grants, recipient workspace/project API denial, sibling isolation, disabled-account denial, revocation, idempotency at the limit, migration with preserved sessions/projects and foreign-key cleanup. Browser inspection covered owner controls and a separate synthetic client account in the local preview. Existing workflow/polling/upload checks passed. No real customer access was granted as part of verification.

Rollout procedure: pause portal writers and fleet reconciliation timers, confirm no unfinished jobs, retain all previous binaries and a consistent SQLite backup, migrate offline, verify record counts/integrity/foreign keys, and restart the previously active services and timers. Customer application workloads are not restarted. A rollback to schema-40 binaries requires restoring the matching database backup with all writers stopped, which loses any changes made after that backup; prefer a forward fix once writes resume.

Live rollout completed with backup `/var/backups/launchstead-client-access-20260923T100142Z` on the VPS. All prior users, sessions, six projects, uploads, publications, active Node releases and domain rows were preserved. Portal and workers restarted successfully. Public portal and the existing custom-domain website returned HTTP 200; anonymous shared-site access returned 401. The live JavaScript matches this release. Existing clients can use Website → Clients; new-client registration still needs operator approval.

## Client invitation release, September 23

Schema 42 adds project-scoped `client_invitations`. Invitation tokens are hashed in the invitation table and encrypted in the existing mail queue. Mail delivery and acceptance both check that the inviter remains an enabled verified workspace owner and the project is not being deleted. Direct grant/removal invalidates pending links for that email. Pending invitations do not reserve client slots; both creation and acceptance check the 20-client cap. Limits are 20 pending invitations per website, 50 creations per day and a one-minute resend interval per recipient.

Focused tests covered recipient isolation, replay, approved new-account registration/verification, preserved signup restrictions, revocation, replacement, expiry, owner demotion, deletion, discarded queued mail, direct grant/removal invalidation, migration and CSRF. The portal package suite and 27 frontend checks passed. Browser verification used synthetic accounts and a fake mail sender to exercise invitation creation, sign-in, explicit acceptance and Shared with me. A signed-out same-tab invitation navigation bug was fixed and covered by a regression check. No real invitations were created or sent during verification.

The live VPS now uses schema 42. Backup: `/var/backups/launchstead-client-invitations-20260923T114116Z`. All installed portal database consumer binaries were upgraded together, existing record counts and integrity were checked, and previously active services/timers restarted. The live code matches the release, the existing custom domain returns HTTP 200, and anonymous invitation access returns 401. Use the matching backup and previous binaries together if a rollback is necessary; restoring the database loses subsequent writes. Customer application workloads were not redeployed.


## Production enablement authorization, September 24

The owner requested continuation of the launch goal and full production enablement. Production rollout is now an explicit deliverable, not merely local implementation. Enable completed features once their actual dependencies and deployment paths work; do not leave functioning features off without a concrete reason. This authorization covers coordinated server/CLI/portal/worker upgrades and feature enablement. Do not represent unfinished GitHub deployment, unconfigured live payment providers or domain resale as enabled. Outreach, actual customer charges and domain purchases retain their explicit authorization boundaries.

Production rollout order:
1. Restore fleet health and preserve a fresh database/binary/config rollback point.
2. Finish candidate retry, deletion and resource cleanup, plus missing-request handling after ambiguous submission. Verify actual Kubernetes behavior for initial publish, update, withdrawal and retry.
3. Upgrade core server/CLI and all portal database consumers together. Enable candidate operations in core and fleet, container hosting in the portal, encrypted credential/environment access and provisioning/cleanup support. Confirm publication and HTTPS on the actual workers.
4. Finish and enable GitHub deployment and the remaining client-workspace launch features. Update landing/onboarding material to match verified production behavior.
5. Enable live hosting subscriptions, merchant payments and domain resale when provider accounts, credentials and lifecycle integration are ready. Record any owner-only account setup that remains. Complete a real paid pilot before claiming the launch goal is finished.

Operational check on September 24: `pi-home` had stopped reporting node status and could not reach the control plane through WireGuard, while direct SSH remained available. Restarting `wg-quick@wg0` restored tunnel traffic and Kubernetes reported the node Ready. This restored connectivity; it did not establish the underlying cause. No feature flags or application binaries were changed during that repair.

Production inventory on September 24 at 07:18 UTC confirmed all three nodes Ready and all eight application Deployments at their expected available/updated replica counts. Core remains schema 6 and portal schema 42; both passed read-only integrity and foreign-key checks. The six portal projects are preserved, with no queued/running publication, Node build or Node deployment jobs at observation time. The reusable `deploy/fleet/rollout-inventory.py` captures aggregate counts, versions, service states and binary hashes without printing credentials. Seven Linux/AMD64 portal/worker binaries built successfully from commit `507ef9b` and are staged locally, not installed. This observation does not replace draining writers and taking fresh consistent backups immediately before migration.

Core commit `4ca89c8` completes local reuse of a withdrawn first deployment: the same app can receive a new request and port after exact recovery and retirement checks. Service rebinding preserves its UID and changes its operation fence, preventing an old recovery from overwriting the retry. Lost reset replies resume safely. Connected database/runtime tests and relevant package checks passed; this remains source-only pending deletion/cleanup, missing-request handling and real-cluster qualification.

Core commit `fe79007` adds resumable deletion for candidate-managed projects, including hidden initial withdrawals. A persistent inactive Service marker blocks new submissions until recorded candidate generations and the legacy workload are drained, routes/runtime secrets and credential/environment records are cleared, and the app is marked deleted. The Service is removed last. Zero-replica workload tombstones remain to prevent late creates; they do not consume running capacity. This requires the existing single-server writer model and a drained coordinated upgrade. Focused local checks and Linux/AMD64 builds passed. No production binaries or flags were changed.

September 25: core `273999b` reclaims older candidate and legacy workload capacity after committing the new applied release. It preserves the selected generation and retained release inputs, skips stale requests or another pending deployment, and retries observed drain through the explicit advance operation. The fleet worker now advances applied receipts before settling the portal job, even after the original activation deadline, without resubmission or recovery of the committed release. Core/fleet checks and portal reconciliation integration checks passed. Production remains unchanged pending lost-submission recovery and real-cluster qualification.

September 25: core `68df8a4` adds missing-submission withdrawal using the original configuration and request ID. Schema 12 persists withdrawal intent atomically with an absent request's binding, before runtime recovery. Both this operation and ordinary recovery block delayed submission/advance after an interrupted recovery; an already-applied release wins the race and is preserved. Additive RPC and CLI support are committed and pushed. Focused DB/server/runtime/CLI checks and vet passed. The next implementation step is fleet integration when the original request cannot be read after its activation deadline (or activation authorization is withdrawn), preserving durable recovery intent and validating the original configuration. CLI errors currently lack typed NotFound information, so do not infer terminal failure from error text. No production binaries, schemas or flags were changed in this step.

September 25 follow-up: fleet recovery is now wired to core missing-submission withdrawal. New operations retain their original YAML. A failed lookup after the activation deadline or withdrawal of activation authorization records recovery intent before calling the new operation, preserving the original request ID. Lost replies resume without re-deploying; applied race winners retain their release and complete workload cleanup. Receipt identity and full preflight state remain mandatory before observation can settle the job. Older operation records lacking original YAML require operator review. Worker/client and portal reconciliation checks passed; no production migration or feature activation has occurred. Next: real-cluster public/private image publication and recovery checks, then coordinated server/CLI/portal/worker rollout.

September 25 cluster evidence: core `eeb4a58` fixes Kubernetes-added empty pod security contexts and TCP Service-port defaults in the candidate path, while preserving legacy manifest compatibility. An isolated real-cluster lifecycle check passed absent-request withdrawal, first-publication retry across both Pi workers, recovery of an update configured with an unhealthy probe, preservation of prior-release health, withdrawn-request advance rejection and project deletion with no app Pods remaining. It used a generated namespace, temporary database and pinned public ARM64 nginx image, not the live portal or core database. Post-check inventory at 07:09 UTC found all three nodes Ready, all eight existing deployments at expected replicas, both database integrity checks clean, and production still on core schema 6/portal schema 42. Private-image credential rotation, public HTTPS/portal publishing, delayed-writer checks and coordinated rollout remain before Docker production enablement.

September 25 HTTPS prerequisite repair (core operations commit `b5840e8`): the live ACME issuer was unready because cert-manager on pi-home could not resolve DNS. The controller and cainjector were moved to the VPS without a version change; the issuer returned Ready at 07:12 UTC. Fresh diagnostic Pods identified DNS failure only on pi-home. Its Flannel interface and remote Pod routes had disappeared following the WireGuard restart despite node Ready status. Restarting k3s-agent restored the interface/routes and fresh Pod lookups of Kubernetes and the ACME hostname succeeded. A systemd drop-in now couples k3s-agent to wg-quick@wg0 restart/stop and orders startup after it on both Pis; only pi-home was restarted. The diagnostic namespace was removed. Inventory at 07:15 UTC shows all three nodes Ready, eight existing deployments at expected replicas, both databases intact and no inventory errors. Production application binaries/schemas and Docker flags remain unchanged. Public HTTPS Docker validation, private registry rotation and coordinated rollout remain next. A private GHCR qualification package is not yet present; the local GitHub CLI has package-write scope, so a dedicated private fixture can be provisioned without requesting another token. Keep credentials out of command arguments/logs and verify private visibility before uploading.

September 25 Docker HTTPS evidence: core `c549ae0` creates the proxy middleware/transport referenced by candidate routes and ensures the configured issuer before TLS routing. An isolated cluster run passed initial publication, trusted public HTTPS (200 plus expected nginx content), recovery with HTTPS preserved, and project deletion in 60.09 seconds. An independent Mac request confirmed the same certificate and content. Its temporary DNS-only record and namespace were removed; the existing issuer remains Ready. Post-check inventory at 07:20 UTC reports all nodes and existing deployments ready, no errors, and unchanged production core schema 6/portal schema 42. Remaining Docker rollout work: private image/credential rotation, delayed independent writer and successful-update/restart checks, followed by the coordinated application upgrade and portal publication verification. This is runtime/HTTPS evidence, not a claim that the portal feature is already enabled in production.


September 25 follow-up: the isolated real-cluster lifecycle check passed a healthy
update after database reopen/controller reconstruction, retirement of its prior
running generation, and direct delayed-writer activation/create rejection after
recovery (50.59 seconds). This recreates server state, not an actual process
crash. Cleanup completed and all eight existing deployments remain healthy.
A dedicated private ARM64 GHCR fixture now exists and anonymous access is denied.
The owner was asked to provide a separate read-only package token for the worker
pull check; the broad publishing credential remains off the nodes. Private pulls
and actual credential rotation remain unverified. Fresh production artifacts and
the coordinated migration/rollback procedure are being prepared in parallel.
Docker remains disabled in production pending the application upgrade and portal
publication verification.


September 25 production upgrade: core `1a00d2e` and admin `81407a8` are now
installed on the VPS: ten binaries (including the configuration-sensitive log
service) and three fleet controllers. All database writers and controller timers
were stopped for consistent backups, then core migrated from 6 to 12 and portal
from 42 to 46. Existing record counts and foreign-key/integrity checks passed.
All previously active services and timers restarted. At 08:42 UTC all three nodes
and eight application deployments were healthy; portal and the existing custom
website returned HTTPS 200, the operator panel returned its expected anonymous
401, and authenticated CLI app listing succeeded. Existing feature settings were
preserved, so Docker/candidate activation remains next, after the private pull
check and portal publication verification. See the [upgrade and rollback
record](production-upgrade-20260925.md). GitHub deployment, live payments and paid
pilot acquisition remain separate unfinished goal items.


September 25 portal follow-up: admin `5e19bcf` is installed across all eight
portal database consumer binaries. Portal schema migrated from 46 to 53, with
core unchanged at `1a00d2e` / schema 12. Owner-visible client access history is
live. GitHub implementation is installed but its connection and automatic
deployment flags remain off pending App registration and real provider checks.
Docker remains off pending private pull and portal publication verification.
At 11:56 UTC all three nodes and eight application deployments were healthy;
existing record counts were preserved and both databases passed integrity and
foreign-key checks. Public portal and the existing custom domain returned HTTPS
200. See the updated [production upgrade record](production-upgrade-20260925.md).


September 25 marketing follow-up: landing-page `f064701` is deployed to the
production Cloudflare Pages project and `launchstead.0xivanov.dev`. The product
page and FAQ now describe live project-scoped read-only client invitations and
searchable internal client labels, distinguishing both from workspace-wide Team
access. The marketing kit and assisted onboarding checklist reflect shipped
access history and revocation. Public content retains private access, no live
payments/domain purchasing, and operator approval for new client accounts.
The production custom domain and Pages URLs returned HTTP 200 with the new
client FAQ using a browser user agent; the first Python-default-agent request
received HTTP 403. No security settings were changed. No outreach was sent.


September 25 merchant payment follow-up: schema 56 separates sandbox and live
merchant records, including account mappings, products, orders, refunds, event
processing and buyer recovery. Existing history migrates to test mode unchanged.
Provider/store mode checks and separate buyer cookies prevent crossover. Full
portal integration checks, provider tests, static analysis and command builds
passed; focused migration checks compare every original column in both directions.
This checkpoint is committed source only. Production remains admin `417691c` /
schema 55. Merchant runtime configuration and live-facing UI are next, followed
by provider setup and a coordinated deployment. Live credentials, GitHub App
setup, private registry qualification and registrar integration remain open.


September 25 merchant runtime follow-up: explicit live/test merchant settings
now connect portal startup, merchant maintenance, provider selection and separate
webhook routes to schema 56. Hosting and merchant selections remain independent.
The buyer shop and merchant controls display selected-mode payment/refund copy;
buyer browser state is separated by mode. Legacy test settings remain compatible,
while conflicting or incomplete live settings are rejected. Source-only status:
production is still `417691c` / schema 55 and no real merchant transaction has
been performed. See [live merchant activation](live-merchant-activation.md).
Live Stripe account readiness and credentials are still required before rollout
activation, followed by independently authorized real payment verification.


September 25 merchant rollout: admin `fa4eda4` is installed in all eight existing
portal database consumer binaries; schema 56 is live. Migration preserved
merchant history exactly. Postflight at 13:37 UTC verified unchanged records,
healthy databases, three nodes, eight app deployments, empty queues and HTTPS
responses. Hosting remains test-mode, merchant sales disabled, GitHub/Docker
disabled. This is deployment of implementation support, not live provider
activation or paid pilot completion. See the production upgrade record.


September 25 portfolio follow-up: customer-portal `5b39d66` is deployed with
status filters, exact client-label selection, unassigned-label filtering and
A–Z/Z–A sorting. Search/type filters combine with these selections. Existing live
versions remain classified live when a new version is merely awaiting publish;
active operations and failures take precedence. Unknown/loading state is never
claimed live. Selected details bypass portfolio filters, and sorting reuses
existing DOM nodes without rebuilding forms or resetting file inputs.

All 56 JavaScript checks passed, including mixed filters, client labels matching
reserved names, removed-label fallback, stable sorting, detail preservation and
workflow classification. Production JavaScript/CSS exactly match the release;
portal and existing custom domain returned HTTPS 200. Only customer-portal was
restarted. Schema remains 56 and workers remain `fa4eda4`. No authenticated
browser visual check is claimed. Previous portal binary is retained at
`/var/backups/launchstead-portal-portfolio-20260925/customer-portal`.


September 25 onboarding and marketing: landing-page `7e884a8` is deployed to
Cloudflare Pages and the public Launchstead domain. The homepage now describes
shipped client/status portfolio filters. A public first-site guide covers project
fit, ZIP preparation, publishing, existing-domain DNS/HTTPS, scoped client access,
one successful update and feedback. It explicitly retains private-access limits
and excludes unavailable integrations, domain purchases and live sales.
The marketing kit links the guide and uses the current portfolio workflow.
Public content was checked after deployment propagation; both the guide and
homepage returned HTTPS 200 with the new copy. No outreach was sent and no pilot
customers or paid outcomes were fabricated.


September 25 NameSilo follow-up: implemented a fixed-OTE sandbox quote reader,
private startup configuration, sandbox labels and stored environment checks in
prepared domain orders. No schema migration. Source reviewed with focused domain
integration checks; not deployed or configured in production. Registration,
renewal, payment fulfillment and live-provider activation remain outstanding.
