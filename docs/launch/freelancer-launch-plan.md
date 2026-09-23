# Launchstead: freelancer-first launch plan

Updated September 23, 2026. This plan narrows the launch audience; it does not declare the original four-part hosting goal complete.

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
- Remaining: visible access audit history, client labels/organization and handover. Existing Team roles are still workspace-wide. Registration remains restricted to the operator-approved allowlist. See [client onboarding](client-onboarding.md).
- Client labels and portfolio organization, then ownership handover after membership/billing implications are designed.
- Shareable deployment summaries with no secrets and no implied access grant.

Acceptance: a client sees only the intended website; cross-project APIs deny access, including logs, uploads, domains and releases.

### 4. Developer deployment paths
- Registry image reference first: Docker Hub/GHCR, port, health check, environment variables, private credential handling, digest pinning and ARM64 compatibility validation before acceptance. Implemented the metadata resolver/check command with bounded requests, digest verification and ARM64 selection; real public metadata probes succeeded for both registries. Local schema 43 also adds disabled-by-default container projects and immutable release storage, with permission rechecks, shared capacity admission and retry-safe digest pins. Local schema 44 adds the deployment queue, cancellation, persisted dispatch intent and authorization rechecks; fleet configuration rendering supports selected ports and health paths. Runtime submission and reconciliation now have a local, explicitly gated worker path with durable revision fences and actual configuration/HTTPS readiness checks. The local portal now provides gated image checking, release history, publishing/restoring and cancellation controls. Browser verification covered a real public metadata check, setup availability refresh, queued publication and cancellation in a disposable database with no fleet worker. Container provisioning, project cleanup and custom-domain port handling are now integrated locally with focused controller checks. Private credentials, environment settings and fleet qualification remain incomplete. This is not deployed or yet a customer deployment feature. Remaining work and evidence are in [registry image deployment](registry-image-deployment.md).
- GitHub connection and deploy-on-push second, with narrow installation access and webhook validation.
- Dockerfile builds later, with resource limits and isolation. Define storage/database support explicitly; do not market all MVP workloads as supported.

### 5. Paid private launch
- Activate actual hosting subscriptions only after business/provider setup, pricing and customer terms are ready.
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
