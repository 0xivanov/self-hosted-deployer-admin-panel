# Customer platform implementation progress

Updated: 2026-09-10. The four-part goal remains active and incomplete. No customer-platform code has been deployed to the live VPS/Pi environment.

## Implemented and verified

`internal/portal/store.go` is an independent customer data layer with a private SQLite database and versioned initial schema. It does not import the deployer CLI bridge or possess administrator credentials.

- Registration atomically creates an unverified account, workspace, owner membership and hashed verification token.
- Password hashing uses Argon2id, individual salts and bounded hashing concurrency.
- Login requires verified, enabled accounts. Password state is rechecked inside the session transaction to avoid issuing an old-password session during a reset.
- Opaque sessions persist across restarts and are stored only as hashes; logout, expiry and account disable deny access.
- Password reset tokens expire and are single use, including concurrent requests; a successful reset invalidates existing sessions.
- Project creation/read resolves workspace membership from a valid session in the same transaction. Cross-workspace access and viewer writes are rejected. Membership removal takes effect without trusting cached browser roles.
- Audit events cover account registration/verification, session creation, password reset and project creation.
- Database creation requires private paths and rejects a newer schema instead of opening it with an older binary.

Validation: `go test -race ./...`, `go vet ./...`, and existing admin-panel build. Dedicated tests cover unverified access, duplicate email normalization, verification replay, session persistence/logout, cross-workspace operations, viewer permissions, membership removal, concurrent reset replay, session/reset expiration, account disable and private/future-schema database rejection.

## Current remaining work

The customer portal is a development service, not a production-qualified public account service. Signup, encrypted mail delivery, membership management, invitations and validated upload storage are implemented as described below. Publishing/build workers, durable releases and rollback, registrar integration, hosting subscriptions and merchant payments remain outstanding. Account recovery/resend, administrator MFA/bootstrap, production abuse controls, storage capacity operations and recovery qualification also remain.

## Customer HTTP portal added

`cmd/customer-portal` is a separate runnable service with no operator or infrastructure credential dependency. It provides login/logout, workspace selection, and creating/listing static or Node project records. The frontend explicitly identifies upload/publication as unfinished.

- Production mode requires HTTPS; the only HTTP exception is explicit disposable localhost demo mode.
- Secure, HttpOnly, host-only SameSite cookies; session-bound CSRF headers; exact Origin/Host checks; actual TLS required outside development.
- Login attempts limited by direct peer IP (forwarded IPs are not trusted); eight concurrent API requests maximum; bounded JSON and headers. Proxy-aware limits are future work.
- Tests verify cross-workspace HTTP reads/writes, CSRF, TLS, DNS rebinding, logout, login throttling, invalid/oversized bodies and closed registration.
- Browser demo verified login, project creation, reload persistence and sign-out. The live admin panel was not changed.

Run `go run ./cmd/customer-portal --demo`, then open `http://127.0.0.1:8791`. Disposable credentials: `demo@example.test` / `demo-only-password`. Demo data is removed on graceful shutdown. Do not expose the demo through a proxy. No customer email or real accounts are provisioned by this command.

## Durable account mail queue

Added `internal/portal/mail.go` and portal schema version 2. `AccountMail.Register` and `AccountMail.RequestReset` commit encrypted mail and account/token changes in the same transaction. Public callers receive no verification/reset bearer token from these wrappers. Token-bearing message bodies are encrypted with AES-GCM using an externally supplied 32-byte key; the key is not stored in SQLite.

Delivery uses persistent leases, deadlines and bounded exponential retries, and checks token validity before sending. Superseded/consumed/expired account tokens suppress stale messages. Delivered or discarded message payloads are cleared. Provider errors are not exposed verbatim. Verification/reset links place tokens in the URL fragment; the frontend still needs the corresponding flows.

Tests cover transaction rollback when queuing fails, encrypted storage, restart recovery, functional verification from delivered mail, delayed retry, stale reset suppression, concurrent worker exclusion, concurrent reset requests, and recovery after correcting an encryption-key mismatch. All delivery tests use an in-process fake sender; no external mail was sent.

Operational limits: the `MailSender` transport and worker command are not configured yet. Preserve the encryption key separately from the database and restore both for queued mail recovery. Delivery is at-least-once: a process crash after a provider accepts mail but before acknowledgment may duplicate an email, while its action token remains single-use. Failed/exhausted queues require an operator visibility/recovery flow before public release.

Migration note: version 2 adds the outbox without changing existing account tables. The earlier portal binary intentionally rejects this newer schema; rolling back across this development migration requires a consistent pre-migration backup while writers are stopped. This migration has not been run against any live customer service.

Next: mail transport integration and queue runner, verification/reset frontend and API, then signup with enumeration-resistant responses and rate limits. Public signup remains closed until this end-to-end flow is tested.

## Signup, verification, reset and SMTP integration

The separate portal now supports opt-in signup, email verification with an explicit action button, password-reset requests and password replacement. Responses do not disclose whether registration/reset emails correspond to an existing account. Reset invalidates existing sessions. Email links use fragments, remove tokens from browser history on load, and do not activate accounts just by landing on the page. Cross-site page navigation is allowed for email links; API origin checks remain enforced.

`SMTPSender` supports implicit TLS or mandatory STARTTLS with certificate verification, context/deadline cancellation and sanitized error reporting. The worker sends encrypted outbox entries and retries failures. Real delivery requires private `--smtp-config` JSON (`address`, `username`, `password`, `from`, `implicit_tls`) and `--mail-key-file` containing 64 hex characters representing the 32-byte encryption key. Preserve that key across restarts and backups. Both files must be mode 0600. `--signup` is explicit and refuses to start without account mail configuration.

Example non-demo arguments, with placeholders to be supplied through private files:

```
customer-portal --listen 127.0.0.1:8791 --origin https://app.example.com \
  --database /private/portal/portal.db --tls-cert /private/portal/tls.crt \
  --tls-key /private/portal/tls.key --smtp-config /private/portal/smtp.json \
  --mail-key-file /private/portal/mail.key --signup
```

Demo mode enables signup into disposable storage and writes messages to private local files, with their directory printed at startup. It never contacts SMTP. A browser check registered a synthetic account, opened its local message link, and explicitly verified it. API tests cover the entire signup→queued mail→verify→login→reset→session invalidation flow and generic duplicate/unknown-account responses. SMTP tests use a local TLS fixture and verify plaintext rejection and header-injection rejection. No real account emails or production traffic were used.

Still needed before public registration: verification resend and recovery from expired/exhausted mail, mail queue health/admin tooling, account invitations/roles UI, operator bootstrap/MFA/recovery, stronger distributed abuse controls and a real provider deliverability test with authorized recipients. This remains an undeployed development portal. Publishing, registrar/domain purchases and both payment integrations remain outstanding.

## Workspace member management

Added owner-only member listing, role changes and member removal to the store, HTTP API and customer workspace screen. These operations affect existing memberships only; they cannot create access without an invitation. The roles are owner, developer and viewer. The API requires an explicit role/removal value, rejecting omitted/null roles rather than treating them as deletion.

Changing a role or removing a member revokes that person's sessions. Authorization uses current database membership, and the workspace must retain another verified, enabled owner before an owner can be demoted/removed. Tests cover self-escalation, cross-workspace mutation, developer access to membership data, viewer writes, access after removal, session revocation, rejected implicit deletion and concurrent owner demotion. The concurrency test proves one of two competing final-owner demotions is rejected.

The customer screen offers owner-only role controls with a confirmation prompt. Invitations remain unavailable; tests use explicit fixture memberships. This is not a completed multi-user rollout: invitation acceptance, operator administration/MFA, verification resend and real email qualification remain. No live service was restarted for this change.

## Workspace invitations

Added schema version 3 with hashed invitation tokens, seven-day expiry, recipient email, workspace, role and inviter ownership. Owners can issue/list/revoke invitations through the API and member screen. Invitation messages commit atomically into the encrypted outbox; the response contains metadata, never the bearer link. Replacement invitations revoke prior links for the same workspace/email.

Acceptance requires an authenticated, verified account whose normalized email matches the invitation. The inviter must still be a verified, enabled owner. Acceptance is transactional and single-use, and cannot overwrite an existing membership. Owner demotion/removal permanently revokes their pending invitations, including if ownership is subsequently restored. Stale/revoked invitation messages are discarded by the mail worker.

Tests cover non-owner issuance, wrong-recipient acceptance, granted developer access, replay, attempts to escalate existing members, revocation, replacement, expiry and owner demotion/restoration. `go test -race ./...`, `go vet ./...` and the customer portal build pass. No real invitation emails were sent.

Current onboarding limitation: invite recipients must sign in with an existing verified account or register/verify first when signup is enabled. Creating an account directly from an invitation while public signup is disabled is not yet implemented. The browser retains a pending invitation in memory through sign-in; a reload after removing the URL fragment requires reopening the original email link. Invitation preview details and dedicated acceptance browser tests remain qualification work.

Schema 3 is a development migration. As with schema 2, earlier binaries reject the newer schema; pre-migration backup recovery is needed for rollback across this change. The live operator service and three-node fleet remain unchanged.

## Project archive validation

Added `internal/projectarchive` as the first upload-pipeline component. It validates ZIP content without extracting or executing customer code. Current development limits: 10 MiB compressed, 32 MiB total expanded, 8 MiB per file, 1000 entries and 16 path levels. It checks actual decompressed streams and checksums, not only ZIP header sizes.

Rejected inputs include traversal/absolute/Windows paths, control characters, symlinks/special files, duplicate/case-colliding paths, file/directory conflicts, encrypted/unsupported archives, repository metadata, uploaded dependencies and common credential filenames. Filename checks are best-effort secret prevention, not proof that project content contains no secrets.

Static projects require root `index.html`. Node projects require root `package.json` with a start script and a supported npm lockfile. These checks establish the upload contract; they do not establish that dependencies resolve or the application runs. Dependency installation and any build/start scripts still require the planned isolated build/runtime environments.

Validation tests cover accepted static/Node fixtures, unsafe paths, links, credentials, collisions, expansion bombs, total expanded-size limits, truncation, entry count, incomplete project contracts and cancellation. Race tests, vet and existing command builds pass.

This component is not yet wired to HTTP upload storage or the browser file picker. Next: authenticated workspace-scoped archive persistence, quotas, upload metadata/UI, then static publication and immutable releases. No uploaded code was executed and no live deployments changed.

## Customer project upload storage and panel controls

Schema version 4 adds private, immutable ZIP uploads. The ZIP bytes and validated metadata are stored together in SQLite so failed transactions do not leave filesystem orphans. This initial storage design supports small projects; object storage migration and capacity/backup qualification remain required before a public hosting rollout. Uploaded code is never extracted, executed or served from the portal origin.

Owners and developers can upload and delete saved archives. Viewers can list upload metadata. API authorization is checked before body intake and again within the final transaction after validation. Uploads are bounded to 10 MiB each, with a workspace-wide limit of 20 archives or 100 MiB of compressed data across all projects. Quota checks and insertion share a transaction. Deleting an unused upload releases its logical quota; SQLite may retain allocated disk pages for reuse. Release references do not exist yet and must prevent deletion of required artifacts when publishing is added.

The customer panel includes a ZIP picker, project-specific instructions, validation results and deletion controls. It explicitly labels projects and uploads as not published. Node uploads require package.json with a start script and package-lock.json; static uploads require root index.html. Content validation does not establish that an application is safe to execute.

Validation: race-enabled Go unit/integration tests, vet, command builds and JavaScript syntax checks. Upload integration tests cover cross-workspace denial, viewer restrictions, invalid/oversized bodies, CSRF/content type, archive persistence, concurrent quota contention, quota across projects, deletion and migration from schema 3 without losing an existing account/session/project. CI now includes integration-tagged tests. A disposable browser demo verified sign-in, project creation and rendered upload controls; an actual browser file-selection/upload interaction is still to be checked. No live VPS/Pi changes or external mail were performed.

Operational caveat: take a consistent pre-migration database backup before upgrading any persistent customer portal. An older binary rejects schema 4, so binary-only rollback is insufficient. These local tests do not qualify backup/restore, public registration, multi-process capacity or production rollout.

## Static runtime release engine

Added `internal/staticsite`, a runtime component with no account database, payment keys or fleet credentials. An operator supplies a private directory dedicated to one site and an exact content-host binding. Static ZIPs are validated, retained under their SHA-256 identity, and served directly from the ZIP without filesystem extraction or code execution. The active release pointer is replaced atomically after file synchronization. Rollback revalidates a retained immutable ZIP and changes the same pointer; restart reloads and checks its integrity.

Requests resolve one complete in-memory release snapshot. The handler supports GET/HEAD, conditional requests, byte ranges and optional extensionless HTML SPA navigation. Foreign hosts and unsafe paths are rejected. Missing assets remain 404 rather than returning SPA HTML. Responses use no portal cookies or API routes. Active decompression requests are bounded to 16; archive file/expansion limits still apply. Retention is limited to 20 ZIPs or 100 MiB per site. Explicit pruning rejects the active release and must later honor control-plane retention references.

Validation: race-enabled integration tests cover publish/redeploy/rollback/restart, corrupt publication and rollback, concurrent readers during release switches, host/path/method isolation, conditional/range/HEAD behavior, private pointer files, release-count limits and active-release deletion protection. Full Go tests, vet and command builds pass.

This component is not connected to the customer portal or running publicly. Next: a durable publishing job and runtime assignment, scoped artifact delivery, a worker that reconciles application state after uncertain outcomes, an independent content-server executable, platform HTTPS provisioning, and a local end-to-end upload/publish/rollback demonstration. One process must own each runtime directory; multi-process fencing and process-crash/disk-failure qualification remain outstanding. Filesystem errors after rename can have an uncertain commit outcome and require reconciliation rather than blind retries. The initial engine does not claim isolated Node builds or runtime hosting, request-rate/bandwidth billing, or production backup qualification. No existing deployment was changed.

## Persistent publication jobs

Schema 5 adds publication jobs and the last successfully acknowledged publication per project. Requests are workspace-authorized, static-project-only and tied to an immutable upload. A repeated request key returns the same job; using the key for another upload is rejected. Only one pending job is permitted per project. Each new job has a monotonically increasing project revision, including requests to restore an earlier upload. Uploads referenced by history cannot be deleted, preserving rollback material.

Trusted worker methods claim a specific assigned project's pending job with a hashed one-minute lease, retrieve its archive/hash, and acknowledge the runtime's observed hash. Expired leases can be reclaimed after restart; an old lease cannot complete the job. Current account and membership permissions are checked at request, claim and completion. Successful acknowledgements advance the active publication; failed acknowledgements leave the last successful publication intact. Browser logout does not cancel an authorized asynchronous job. Customer HTTP routes do not expose worker methods or enable publishing yet.

Validation: race-enabled tests for tenant boundaries, idempotency, conflicting/pending requests, artifact retention, lease recovery after database reopen, stale completion, hash mismatch and revoked write permission. A local integration test passes saved uploads through job claims into the real static runtime, publishes a second version, records a failed job without changing the active version, then restores the first immutable upload. Full Go tests, vet and builds pass.

Remaining before enabling the Publish button: operator-assigned runtime mapping, a separate worker process and authenticated transport, runtime enforcement of revision fencing, job cancellation/reconciliation after permission revocation or uncertain remote outcomes, lease renewal for longer work, bounded history/retention cleanup, and production content HTTPS. The local integration test is sequential and does not prove safety against overlapping remote workers. No customer portal or live node was upgraded. Before a future persistent portal upgrade, back up the database consistently; schema 5 requires its matching binary or restoration of the pre-upgrade database.

## Runtime revision fencing and exclusive ownership

The static runtime now supports `PublishRevision`: a positive project revision is persisted atomically with the active release hash. Older revisions and same-revision/different-archive requests are rejected. Identical retries are idempotent. Restoring old content requires a new publication revision, so the legacy publish/rollback methods cannot bypass an established fence. Existing plain-hash active pointers remain readable and are upgraded on the first versioned publication. Once upgraded, older runtime binaries cannot read the new pointer format; retain a consistent pre-upgrade runtime backup for binary rollback.

Each site directory is held by one runtime instance using a nonblocking operating-system file lock. The lock rejects symlinks, requires private permissions, is released on Close/process exit, and prevents two instances from racing pointer writes. Closed instances cannot publish, roll back or prune. The runtime reconciles a visible new pointer into memory when a directory-sync error occurs after rename, while returning the uncertain result for the worker to reconcile before acknowledging success. An identical retry synchronizes the directory again.

Race-enabled tests verify stale workers, conflicting retries, versioned rollback, persistence after reopen, concurrent revision ordering, exclusive ownership and lock release. The portal-to-runtime integration test now uses the actual publication revision rather than the legacy publish method. Full Go tests, vet and builds pass. These checks run locally on macOS; Linux CI and process-kill/power-loss qualification remain additional evidence to collect. Production runtime directories must be on local filesystems with qualified locking and rename semantics; network filesystem behavior is not assumed.

Next remains a separate worker/runtime transport and project assignment, end-to-end UI publishing, HTTPS content hosting and failure/permission-revocation reconciliation. No live service was restarted.

## Private static runtime transport

Added `internal/staticpublish`: an HTTPS management handler and client scoped to one operator-assigned project, hostname and 32-byte random credential (64 hex characters). The management handler is separate from the public content handler. It exposes only publication and atomic active-release/revision observation, with no filesystem path, fleet operation or arbitrary project selection. Authentication and project checks precede body intake; requests carrying browser Origin headers are rejected. Publications are bounded to 10 MiB and one concurrent request, and use the runtime's persisted revision fence.

The client verifies certificates with system trust or an explicit management CA, disables proxy inheritance, refuses redirects, bounds request duration and response parsing, and verifies the acknowledged revision and archive digest. Provider/body errors are not returned verbatim. Transport failures remain uncertain outcomes: callers must observe/reconcile or retry the same fenced revision instead of recording a deployment failure that might already have reached the runtime.

Race-enabled integration tests use a real local TLS server and static runtime. They cover publication/status, stale revisions, wrong project/key, browser origins, foreign hosts, plaintext requests, untrusted certificates and redirect refusal. Full tests, vet and builds pass. No provider credentials or live requests were used.

Still needed: the standalone management/content server with bounded listeners and private credential loading; the assigned-project worker loop and portal routes/UI; permission-revocation and uncertain-outcome reconciliation; runtime credential rotation and Linux deployment qualification. Do not mount the management handler on a public content listener or expose it through the customer portal. HTTPS alone does not replace an operator-controlled management network boundary. No public publishing capability was enabled.

## Assigned-project publication worker

Added `internal/publisher` and `cmd/publication-worker`. The standalone command loads a private assignment JSON containing `endpoint`, `project`, `token` and optional `ca_file`, opens the private portal database and polls only the assigned project. Invocation: `publication-worker --database /private/portal/portal.db --assignment /private/worker/assignment.json`. The assignment must be a regular mode-0600 file, no larger than 16 KiB; unknown/trailing JSON is rejected. No environment credential fallback or public HTTP worker endpoint exists. The process handles SIGINT/SIGTERM and logs only generic reconciliation notices.

Each attempt is bounded to 45 seconds, below the one-minute lease. The worker sends the claimed immutable archive and revision over the verified runtime transport, checks the acknowledged hash/revision, then records success under the current lease. Remote errors leave the job running for retry because the runtime may already have committed. Retrying Publish with the same revision also completes a potentially uncertain directory sync; status observation alone would not establish durability. Completed jobs are not claimed again.

Fixed permission-revocation recovery: an already-running job whose actor loses write access now remains pending for operator reconciliation instead of being marked failed and silently allowing a new publication past an unknown remote outcome. A queued job that never started can still fail before sending content. Automated compensation/cancellation and the operator reconciliation interface are not implemented yet; do not enable public publishing until this path is complete.

Validation: a race-enabled test connects the real portal store, worker, HTTPS transport and static runtime, simulates a lost success response, confirms the job remains pending while content has changed, prevents reuse of an unexpired lease, expires the lease as a test fixture and proves an idempotent retry acknowledges the same revision. Full race-enabled tests, vet and all command builds pass. No live runtime credentials, email or fleet services were used.

Next: standalone management/content runtime executable, assignment provisioning, publishing and release-history controls in the portal, operator reconciliation, platform HTTPS and process-crash qualification. The worker command is implemented but not installed or running on any live node.

## Standalone static runtime process

Added `cmd/static-runtime` and `internal/staticruntime`. The process loads a private bounded JSON config and private TLS keys, owns one site directory, and serves independent content and authenticated management HTTPS listeners. Management binding rejects wildcard, DNS and public addresses; only explicit loopback/private IPs are accepted. Startup binds both listeners before reporting readiness and cleans up if either fails. Read/header/write/idle limits and graceful signal shutdown are configured. Neither listener imports the customer database or fleet APIs.

A real local TLS integration test starts both listeners, verifies the public listener rejects publishing and management requests need credentials, publishes a static ZIP, retrieves its content, stops the process and reopens the runtime directory to verify persistence and lock release. Additional tests reject unsafe management bindings and public/unknown configuration. Full race-enabled tests, vet and command builds pass.

Usage and configuration are documented in `docs/static-runtime.md`. No runtime was installed on a live node and no public certificate or DNS record was provisioned. Remaining: assignment provisioning, publishing/release-history UI, operator reconciliation, certificate automation, Linux crash qualification and production deployment. The four-part goal remains incomplete.

## Assigned publishing controls and release history

The customer portal now has authenticated publication/history routes and upload-level Publish/Restore controls. Publishing remains disabled by default. Operators may pass `--publication-sites /private/portal/publication-sites.json`, a private bounded JSON map of static project IDs to their assigned HTTPS content origins, for controlled development/pilot use. The portal copies and validates this configuration at startup; customers cannot modify it or supply a runtime endpoint. Worker credentials and management endpoints remain in the separate worker configuration. Automatic runtime provisioning and a durable assignment administration workflow are still needed.

Example mapping structure: `{"64_HEX_PROJECT_ID":"https://customer-site.example.net"}`. Replace the key with the actual project ID and configure a distinct content domain, matching runtime and worker before enabling it. Same-host portal/content assignments, non-HTTPS URLs, credentials and URL paths/query/fragment are rejected. Operators must still ensure a separate registrable content domain and correct project/runtime mapping; a syntactically valid URL is not infrastructure qualification. No mapping was installed in a live service.

Owners/developers can request a saved upload or restore previously successful content as a new revision. Viewers can inspect history but cannot publish. One pending job prevents another request. The browser retains its request key when retrying a failed button action and offers explicit status refresh. History shows queued/running/succeeded/failed and the current release, with a site link only when a successful publication exists. Running status explicitly allows for pending reconciliation. The active release is included even outside the latest 100 jobs, avoiding false unpublished status after many failed attempts.

Validation: HTTP integration tests cover configuration gates, immutable copied configuration, cross-workspace denial, viewer permissions, CSRF, repeated request identity and acknowledged active-release history. Additional tests cover unsafe content URLs and an active release older than the recent-history window. Race tests, vet, command builds and JavaScript syntax checks pass. Browser interaction testing of the new publish/restore controls and a complete multi-process demo remain pending.

Next: operator reconciliation of uncertain/revoked jobs, durable assignment management and a browser-driven upload/publish/rollback demo using the separate worker/runtime processes. No live node, DNS or payment system was changed. This feature is a development milestone, not production qualification or completion of the four-part goal.

## Owner recovery for stalled publications

Added an owner-only Resume publication action in the panel and API. A current verified workspace owner may explicitly adopt a running job after its lease expires, keeping the exact immutable upload, job ID and revision. The worker then retries the same fenced publication and records its outcome under the owner's authority. This handles a developer losing permission after the runtime may already have applied the release. Resume never silently substitutes another upload or marks an unknown outcome as failed.

The database transaction verifies owner role, workspace, upload identity, job state and lease expiry, invalidates the prior lease, and records the new owner plus previous actor in the audit trail. A repeated resume before the worker claims it is a no-op. Active workers, developers/viewers, foreign workspaces, completed jobs, changed payloads, missing CSRF and projects without a portal assignment are rejected. The UI explains that resuming publishes the saved website files using the owner's permission.

Race-enabled tests exercise permission revocation, adoption, retained revision, old-lease rejection, successful acknowledgement and an exactly-once adoption audit. HTTP tests cover assignment, tenant and CSRF gates. Full tests, vet, command builds and JavaScript syntax checks pass. Browser verification of the recovery control remains pending.

This is a resume/reconcile path, not a cancellation or compensating rollback system. If the owner does not want the pending content published, an operator workflow must establish runtime state and advance its fence before safely replacing or cancelling it. That workflow, complete multi-process browser qualification, durable runtime assignment administration, billing, registrar integration and isolated Node execution remain required. No live changes were made.

## Multi-process static publication qualification

Added `integration/static_flow_test.go`, included by the existing integration-tagged CI test command. It builds race-enabled customer portal, publication worker and static runtime binaries, then runs all three as separate operating-system processes against disposable private directories and locally generated TLS certificates. TLS verification remains enabled using an isolated test trust pool; no system certificate trust, DNS, provider account or live node is changed.

The test creates a verified synthetic account/project as a setup fixture, signs in through the real HTTPS portal, uploads two ZIPs through its HTTP API, requests publication and waits for worker acknowledgement. It verifies both versions through the separate content listener, gracefully restarts portal and runtime while the worker remains separate, verifies session/content persistence, then requests and verifies rollback to the first saved upload. It also checks child-process exits and graceful cleanup. It passed locally in about 22 seconds including binary builds. Go vet and whitespace checks passed.

Run: `go test -race -tags=integration ./integration -v`. The test uses ephemeral local ports and its own credential/certificate files. It exercises entrypoints, private configuration loading, database sharing, authenticated network transport and durable release state rather than only in-process library calls. It does not exercise account signup/email, browser file selection, UI click flows, abrupt process kills, disk loss, Linux deployment, production HTTPS renewal, cancellation, payments, domains or Node sandboxing. Those remain explicit qualification/implementation work; the goal is still active.

Next: browser-driven validation of publish/restore/recovery controls, cancellation/compensation for unwanted pending releases, durable assignment administration, and the remaining payments, registrar and Node hosting phases. All test processes were stopped and temporary data cleaned up; existing deployments are unchanged.

## Browser publishing verification

Used a disposable local browser fixture with a synthetic verified account, assigned project, real portal UI, worker and authenticated TLS runtime transport. Through browser controls, selected/uploaded a ZIP, published revision 1, uploaded/published a second ZIP as revision 2, and restored the first upload as revision 3. The panel showed each queued request and successful current revision after explicit status refresh. The fixture used no real mail, provider account, public DNS or live node.

The browser check found two misleading labels, now fixed: a queued first publication no longer says Ready to publish, and the currently active upload no longer offers Restore. Pending status is explicit, the current upload button is disabled and labelled Current upload, and a single-file archive uses singular wording. Rebuilt the disposable fixture and verified the revised upload, queued and current states in the browser. JavaScript syntax and whitespace checks pass.

Scope: browser interaction was verified for file selection/upload, publishing, redeploy and restore. The browser fixture uses HTTP only for the loopback development portal, HTTPS with an isolated trust pool for worker/runtime traffic, and a placeholder public site link which was not opened. Actual public content delivery over HTTPS and separate process restart/rollback are covered by the preceding multi-process test, not this browser session. Recovery/resume dialogs, negative browser cases and production-origin/certificate behavior still need browser qualification. Temporary harness files, ZIPs and processes were removed after the checks.

## Stripe test-mode hosting adapter

Added `internal/hostingbilling` using the official `stripe-go/v86` SDK pinned to v86.4.2. The adapter accepts only `sk_test_` secret keys, server-configured plan-to-Price IDs and fixed HTTPS success/cancel URLs on one origin. Checkout uses subscription mode, a supplied server-owned Customer ID, one configured price and a durable opaque idempotency/reference key. It does not accept customer-selected amounts, redirects or Connect account scope. Returned sessions must be test-mode sessions hosted by checkout.stripe.com. SDK logging and telemetry are disabled; provider errors are sanitized and uncertain outcomes retain their request identity for later reconciliation.

Webhook verification uses the SDK against the unchanged raw body, including signature tolerance and API-version checks. Additional gates bound body/header size, reject ambiguous/far-future timestamps, require explicit livemode=false, and reject connected-account/context events from the hosting integration. Verification returns a payload hash and event identity for a future durable inbox; it does not grant workspace entitlements. No HTTP billing endpoint, customer/intent database mapping, subscription reducer, invoice reconciliation or billing UI is installed yet.

Tests use local fake Stripe responses and synthetic signed events. They verify subscription form fields, idempotency key and reference, pinned API version, fixed prices/URLs, absent connected-account headers, live-key rejection, sanitized provider errors, raw-body tampering, stale/future signatures, API mismatch, rotating signatures, connected-account rejection and size limits. No Stripe account or real secret was used, no external payment call was made, and no charge is possible through the current unconnected adapter. Actual Stripe sandbox contract testing remains pending.

Implementation references checked on 2026-09-10: [Stripe webhook verification](https://docs.stripe.com/webhooks?lang=go), [Checkout Session creation](https://docs.stripe.com/api/checkout/sessions/create), [idempotent requests](https://docs.stripe.com/api/idempotent_requests), and [official Go SDK](https://github.com/stripe/stripe-go). The SDK API version must match the future webhook destination; mismatch checks are not disabled.

Next: durable owner-authorized checkout intents/customer mappings, a deduplicated webhook inbox, subscription/invoice reconciliation and owner billing UI. Connect merchant onboarding/sales/refunds remain a separate implementation. Hosting entitlements must never rely on a browser redirect or metadata alone. Domain resale, Node sandbox execution and the remaining recovery/qualification work also remain open.

Validation for this adapter change: full race-enabled Go suite including the three-process static test, Go vet and all command builds passed. Live deployments remain unchanged.

## Durable test-mode billing webhook inbox

Schema 6 adds `billing_events` with provider identity/type/time, canonical object payload, fingerprint, pending state and receipt time. `AcceptBillingWebhook` verifies the raw Stripe signature and explicit test/platform scope before writing. Its transaction deduplicates event IDs, rejects conflicting payloads without replacing the original, and enforces 10,000 retained events or 100 MiB of object payload. Duplicate receipts still succeed when storage is full because they need no new allocation.

Identity fingerprints include type, provider creation time and canonical object data, excluding delivery-envelope counters. Canonicalization preserves JSON numbers without converting large monetary integers through floating point. The original signed-body digest remains available from the verifier; the inbox uses semantic event identity for delivery deduplication. Payloads may contain billing details and remain in the private portal database, not browser responses or logs. Retention cleanup and receipt tombstones still need design before public operation.

A dedicated, unmounted HTTPS webhook handler validates host, method, signature and body limits with four concurrent intake slots. It returns 204 only after durable insertion or verified duplicate recognition. Invalid signatures receive 400; database, identity conflict and capacity errors receive a generic retryable 503. This handler belongs on a dedicated route outside the browser CSRF/session API; no deployed endpoint was configured. Inbox acceptance grants no hosting entitlement.

Tests cover concurrent duplicate intake, reopening the database, changed delivery counters, conflicting customer data, exact large-number preservation, tampered bodies, missing signatures, plaintext/foreign/browser requests, unavailable storage and count limits. Focused race tests, vet and command builds passed. Before a future persistent portal upgrade, take a consistent database backup; schema 6 cannot be opened by older portal binaries without restoring the pre-upgrade database.

Next: owner-authorized billing customer/checkout intent records, reconciliation workers and subscription/invoice handling, webhook route configuration and billing UI. Paid hosting access, Connect merchant sales, domain resale and Node runtime isolation remain incomplete. No live credentials, charges or deployment changes were made.

The full race-enabled suite, including the three-process static publication test, also passed after the inbox migration.

## Workspace billing customer identities

Schema 7 adds one durable billing-customer request per workspace, containing a generated request ID, requesting owner, normalized owner-email snapshot, creation time and an optional unique Stripe Customer ID. Request/read operations require a current workspace owner. Concurrent requests return the same identity; developers/viewers and other workspaces cannot request or inspect it. Request IDs are excluded from customer JSON serialization.

Trusted worker access stops pending customer creation after 23 hours, before Stripe's documented minimum 24-hour idempotency retention window, and when the original requester is no longer a verified, enabled owner. Expired/uncertain requests require provider reconciliation rather than generating a new customer blindly. Binding an authenticated provider response is idempotent, cannot replace an existing customer and cannot bind one customer to two workspaces. A response already obtained may be retained even after permission revocation so the external object is not forgotten. No subscription or hosting access is granted by customer creation.

The Stripe test adapter now creates Customers using the persisted email, request metadata and idempotency key, rejecting live responses. Local fake-provider tests verify those exact request fields. Store tests cover concurrent creation, restart persistence, owner-only read/write, stable request identity, cross-workspace customer collisions, immutable binding, expired requests and revoked owner work. Focused race tests, vet and command builds passed.

These are internal customer-identity operations, not exposed billing routes or a running worker. Next: checkout intents tied to these customer identities, authenticated provider session binding, event-to-workspace reconciliation, subscription/invoice handling and owner billing UI. Long-lived uncertain customer requests still need a reconciliation tool. Schema 7 requires a consistent pre-upgrade backup for rollback to an older portal binary. No live credentials, charges or existing deployments were used or changed.

The full race-enabled suite, including multi-process static publishing, also passed after the billing customer migration.

## Durable hosting checkout intents

Schema 8 adds operator-configured hosting plans and checkout intents. A current owner requests a plan for their workspace; the store resolves the workspace's bound Stripe customer and snapshots the enabled plan's Price ID before any provider request. Concurrent requests reuse one active intent. A competing plan cannot replace it, and plan edits do not mutate an existing intent. Pending/open/completed intents occupy the workspace's active checkout slot until verified lifecycle processing explicitly transitions it.

Worker access rejects pending requests after 23 hours, revoked/disabled owners and a changed or disabled plan. The Stripe adapter's `CreatePinnedCheckout` independently compares the saved price with its configured plan before making a network call. The intent ID is the persistent idempotency/reference key. A provider acknowledgement may bind only one test Checkout Session and an HTTPS checkout.stripe.com URL; session IDs are unique across workspaces and bindings cannot be replaced. Customer-visible reads require owner access. Binding changes pending to open, never paid or active hosting.

Tests cover concurrent request identity, persistence, foreign/developer denial, unconfigured plans, immutable customer/price snapshots, pending plan conflicts, repeated provider acknowledgement, cross-workspace session reuse, untrusted checkout links, expired requests, revoked owners and price-change rejection before contacting the provider. Focused race tests, vet and all command builds passed. Tests use local fake-provider responses only.

Next: customer/checkout worker orchestration, verified checkout completion/expiry and subscription/invoice reconciliation, owner billing routes/UI and actual Stripe sandbox qualification. Subscription upgrades/cancellation and retry reconciliation beyond the provider retention window remain outstanding. No paid entitlement logic, automatic expiry or live checkout endpoint was enabled. Schema 8 requires a consistent pre-upgrade backup for older-binary rollback; no live portal database was migrated.

The full race-enabled suite, including separate-process static publishing, passed after schema 8 and checkout snapshot changes.

## Checkout event reconciliation and subscription identity

Schema 9 adds subscription identity records linked to the acknowledged checkout, workspace, customer, saved plan/price and first verified inbox event. `ProcessBillingCheckoutEvent` handles completed/expired checkout events only after raw-signature verification and durable intake. It matches the stored session ID, customer ID and client reference together; event metadata cannot establish workspace ownership. The object must explicitly be a test-mode subscription Checkout Session.

An event arriving before its session acknowledgement remains pending for retry. Completion atomically records the subscription identity, marks the checkout completed and consumes the receipt. Reprocessing the same event is a no-op. Subscription IDs cannot be replaced on a checkout. Recorded subscriptions start as awaiting_reconciliation, with no paid hosting entitlement. Current subscription/price/invoice state must still be fetched and checked before enabling paid access.

Verified expiry marks an open checkout expired and allows a new intent. A delayed expiry cannot downgrade a completed checkout, while a conflicting completion after recorded expiry stays unresolved rather than silently reactivating old checkout data. Other billing event types remain pending for their future processors. No browser redirect is treated as payment confirmation.

Race-enabled tests cover early webhook delivery followed by binding, wrong customer/reference, concurrent duplicate processing with one audit transition, subscription identity retention, delayed expiry, conflicting subscription replacement and expiry followed by a fresh checkout. Focused tests, vet and command builds passed. All provider events in these tests are synthetic and locally signed.

Next: subscription/invoice retrieval and reconciliation, billing worker scheduling/retries, owner billing controls and actual Stripe sandbox tests. Refunds/cancellation, Connect merchant flows, domain resale, Node hosting and remaining deployment qualification are still open. Schema 9 requires the matching portal binary or restoration of a consistent pre-upgrade backup; no live database was upgraded.

The full race-enabled suite, including the separate portal/worker/runtime publication test, also passed after schema 9 and event reconciliation changes.

## Current subscription state retrieval

The test Stripe adapter now retrieves a subscription with its latest invoice expanded. It checks the saved subscription/customer/price identities, a single quantity-one hosting item, valid billing periods, supported statuses and invoice customer/subscription ownership. Live objects, truncated item lists and unexpanded invoice references are rejected. Cancellation and paused collection are retained in the returned snapshot.

This read produces evidence for reconciliation only. It does not grant hosting access, persist subscription changes or override webhook state. The reconciliation worker still needs durable concurrency control, invoice/payment policy, refund/dispute handling and retry scheduling before it can apply entitlements.

Local fake-provider tests cover matching subscriptions, foreign customer/price/invoice identities, live responses, unsupported quantities, incomplete item lists, trial subscriptions without invoices and past-due state. No real Stripe credentials, charges, live database migrations or deployment changes were involved.

Validation: the full race-enabled integration suite, vet, command builds and whitespace checks passed.

## Durable subscription observations

Schema 10 adds a reconciliation generation and provider snapshot to each saved subscription. The trusted reconciliation operation increments the generation before requesting the current subscription with its persisted customer and price. It saves a response only if that generation is still current and the response identity and observation time match the request. A later-started check fences out earlier results, including when the later check fails. Failed checks retain the previous observation and its timestamp.

Workspace owners can read their subscription observation; developers and other workspaces cannot. These internal methods are not mounted as HTTP routes. Snapshots are evidence only: the subscription remains awaiting_reconciliation and no paid access is granted. Provider field validation lives in the authenticated Stripe adapter. Future entitlement policy must account for observation freshness, invoices, refunds and disputes.

Race-enabled tests exercise overlapping reads finishing out of order, failed lookups, foreign identities, stale observations, workspace/role boundaries and persistence after reopening the database. Existing upload migration tests also exercise upgrading an older schema. Schema 10 requires a consistent pre-upgrade backup for rollback to older portal binaries. No live database, credentials or deployments were touched.

Next: wire the billing worker and owner-facing controls, implement the payment/access lifecycle and qualify against Stripe sandbox. Domain resale, merchant sales and isolated Node hosting remain open parts of the goal.

Validation passed: full race-enabled integration suite, including the separate portal/worker/runtime publication flow; vet; all command builds; whitespace checks.

## Durable test billing worker

Schema 11 adds a durable billing task queue. The worker discovers saved customer/checkout requests, supported verified checkout inbox events and subscription identities. It leases one task for 60 seconds, bounds work to 30 seconds, retains request idempotency keys, retries failed work with 30-second to one-hour backoff and periodically refreshes subscription observations. Completed one-shot tasks are retained as done. Canceled acknowledgement leaves the lease for recovery. Existing owner, price and 23-hour create guards remain in effect.

The new billing-worker command loads a private test-only Stripe configuration and opens the customer portal database. It does not modify configured plan rows or mount HTTP endpoints. Setup and limitations are described in billing-worker.md. Paid entitlement policy, owner payment screens, plan administration, webhook mounting, operator failure visibility and retention are still needed.

Tests cover customer creation with an uncertain first result, durable retry after store reopen, exact saved customer/plan/price/request values, checkout event processing, periodic subscription retrieval, concurrent worker exclusion and recovery after a provider result cannot be acknowledged. Tests use a local fake provider only. The full race-enabled integration suite, vet and all command builds passed; the added cancellation recovery test also passed with race detection.

No live database was migrated and no provider credentials, charges or deployments were used. Schema 11 needs a consistent pre-upgrade backup for older-binary rollback. The broader domain resale, merchant commerce and isolated Node hosting requirements remain active.

## Owner billing request API

The customer portal now has opt-in test billing endpoints for customer identity requests/reads, checkout requests/reads and subscription observations. They run behind existing host/TLS, session, origin and CSRF checks, then enforce workspace ownership in the store. The default is disabled; --test-billing enables the routes. Browser input cannot set provider customer IDs, prices, URLs or acknowledgement state. Worker-only identifiers remain excluded from checkout JSON.

Tests cover owner requests, anonymous access, foreign origins, missing CSRF, foreign workspaces, developer restrictions, injected customer/price fields and disabled-by-default behavior. Customer payment screens, plan administration, webhook wiring, payment entitlement rules and actual Stripe sandbox qualification remain outstanding. No live deployment or provider call was made.

Validation passed: the full race-enabled integration suite, vet, all command builds and whitespace checks.

## Mounted test webhook intake

The customer portal can now mount the dedicated Stripe test webhook at /webhooks/stripe-test, using --test-billing and a private --test-webhook-secret-file. Configuration requires non-demo HTTPS mode. The exact route dispatches to the existing signature/host/TLS/body-limit verifier before browser-session middleware; browser billing routes keep their existing authentication and CSRF checks. Unconfigured, query-bearing and encoded alias routes are rejected.

An integration test sends a locally signed checkout completion through the mounted HTTP handler, verifies duplicate acknowledgement and runs the worker to complete the matching saved checkout. Negative cases cover missing signatures, browser origins, plaintext, foreign hosts, GET requests, aliases and unsafe configuration. No provider endpoint was registered and no live deployment or credentials were used. Customer payment screens, operator plan controls, full billing lifecycle and actual sandbox qualification remain next.

Validation passed: full race-enabled integration suite, vet, command builds and whitespace checks.

## Operator plan configuration and owner catalog

Added billing-plan for explicit local plan enable/disable and Stripe test Price mapping against an existing portal database. It requires all values and refuses a missing database file. Existing checkout snapshots remain unchanged and pending worker requests retain plan/price guards. The command makes no provider call; test-account price verification remains part of integration qualification.

The opt-in billing API now exposes enabled plan identifiers to workspace owners, excluding disabled plans and provider Price IDs. This is a catalog of identifiers, not a price quotation. Customer screens still need accurate provider-backed amounts and intervals. Tests exercise the command's enable/disable behavior and invalid arguments, plus owner catalog filtering and developer denial. No live settings or deployments were changed.

Validation passed: full race-enabled integration suite, vet, all command builds and whitespace checks.

## Provider price verification

The test Stripe adapter can now retrieve the configured plan Price and normalize its fixed amount, currency, recurring interval/count and tax behavior. It verifies the saved price mapping before contacting the provider, expands currency options and rejects live/inactive/deleted prices, wrong identities, one-time or metered prices, fractional minor-unit amounts, tiers, custom amounts, quantity transforms and alternate currency options. A matching expanded default currency is accepted. Amounts retain their currency minor units; no universal two-decimal formatting is assumed.

This is provider verification only, not a final invoice/tax quote or a customer screen. Persisting verified catalog data, refresh/freshness handling and connecting it to the payment UI remain next. Checkout localization and final tax totals also need explicit qualification. The official Stripe price retrieval guidance was checked: https://docs.stripe.com/products-prices/manage-prices?dashboard-or-api=api.

Local fake-provider tests cover valid monthly/free/zero-decimal-currency prices, expanded default currency, unsupported price modes and rejection of configured price drift before network access. No real Stripe request, charge or deployment change was made.

Validation passed: full race-enabled integration suite, vet, command builds and whitespace checks.

## Durable verified price catalog

Schema 12 adds provider price observations, refresh scheduling and generation fencing to hosting plans. The billing worker refreshes one due enabled plan per loop, alongside its existing task queue. Each lookup has a 30-second deadline and reserves the plan for 60 seconds. Successful observations refresh after five minutes; failures retry after the reservation expires. An operator price or enabled-state change clears the saved price, schedules refresh and invalidates in-flight responses. Reapplying unchanged configuration preserves the observation.

The owner-only /api/billing/offers endpoint returns enabled plans with optional verified base-price details. Missing, future-dated and observations at least fifteen minutes old are hidden as unavailable. Provider Price IDs remain private. These observations do not change checkout amounts or grant access; checkout remains the final price/tax confirmation.

Tests cover persistence after reopening, refresh intervals, failed refresh backoff, stale-price suppression, immediate invalidation on reprice/disable, an in-flight lookup racing a plan edit and workspace/developer boundaries. No live database, Stripe request, charge or deployment was used. Schema 12 requires a consistent pre-upgrade backup for older-binary rollback. Payment screens and provider sandbox qualification remain next, with the broader domain, commerce and Node requirements still active.

Validation passed: full race-enabled integration suite, vet, command builds and whitespace checks.
