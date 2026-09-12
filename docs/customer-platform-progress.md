# Customer platform implementation progress

Updated: 2026-09-11. The four-part goal remains active and incomplete. No customer-platform code has been deployed to the live VPS/Pi environment.

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

The customer portal is a development service, not a production-qualified public account service. Signup, encrypted mail delivery, membership management, invitations, static publication, Node build/runtime components and test-mode hosting billing are implemented to the stages documented below. Remaining work includes the connected production Node lifecycle, paid-plan enforcement and provider qualification, registrar purchases/renewals, merchant payments, account recovery/resend and administrator MFA/bootstrap, production abuse controls, storage operations and full pilot/recovery qualification.

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

## Customer test billing screen

The portal now shows an owner-only billing panel when test billing is enabled. It supports requesting a billing account, displaying verified plan prices, selecting a plan, refreshing pending work and opening an acknowledged Stripe test checkout. Missing prices have no selection button. Completed checkout is labeled as awaiting billing reconciliation, never active hosting. The status endpoint returns the current workspace checkout without provider customer/price identifiers, enabling recovery after page reload. Workspace changes invalidate stale UI responses.

/billing/success and /billing/cancel return to the portal shell. Query parameters and redirects do not fulfill checkout. Currency rendering follows Stripe charge units, including zero-decimal and ISK/UGX exceptions; source: https://docs.stripe.com/currencies. Final tax and total confirmation remain on Stripe.

Browser verification used disposable local data: signed in, requested billing, observed pending setup, displayed a synthetic EUR 15 monthly price, selected it, observed pending checkout and verified the rendered ready-checkout link. The external checkout link was not opened. The temporary server and browser tab were closed. Full race-enabled tests passed before the final status/return-route assertions; focused tests, vet, builds and JavaScript syntax checks were then run. Actual Stripe checkout, mobile/role-switch edge cases and complete payment lifecycle qualification remain outstanding. No live deployments or real payment provider calls were made.

## Subscription event refresh signals

Verified customer.subscription.created/updated/deleted events now enter the worker queue. Processing requires an already-bound subscription and its exact customer. Events arriving before checkout binding remain pending; wrong customer identities are rejected. Embedded event status and timestamps are not applied as current provider state.

A matching event atomically clears the prior observation, increments its reconciliation generation, invalidates an old task lease, schedules an immediate subscription read and marks the inbox receipt processed. This prevents an in-flight older lookup from restoring stale state. Duplicate receipts do not invalidate observations produced after their first processing.

Tests cover early delivery, binding/retry, foreign customer rejection, a delayed lookup racing a cancellation signal, duplicate events and current provider state differing from event payload status. Invoice/refund/dispute processing and paid-hosting activation remain incomplete. No live credentials, requests, charges or deployments were involved.

Validation passed: full race-enabled integration suite, vet, all command builds and whitespace checks.

## Invoice event reconciliation signals

The worker now discovers paid, failed, authentication-required, voided, uncollectible and finalized invoice events. The shared refresh processor validates the explicit test-mode invoice object, its subscription parent and its customer against the saved hosting identity. Standalone invoices, unknown subscriptions, foreign customers and account-scoped customer identities stay unresolved. Neither event amounts nor status can establish workspace ownership or grant access.

Matched invoice events use the existing atomic invalidation and rescheduling path, so a fresh provider observation decides current state. Tests run every supported event through durable intake and worker discovery, including negative identity/mode cases, and verify that subscription access remains awaiting_reconciliation. Refund/dispute accounting, paid-hosting activation and actual Stripe sandbox qualification remain incomplete. No live provider, database or deployment was changed.

Validation passed: full race-enabled integration suite, vet, all command builds and whitespace checks.

## Owner subscription management integration

Added test-only Stripe customer portal sessions with pinned configuration, customer and return URL. Provider response validation rejects live, foreign customer/configuration/return identities, connected-account scope and untrusted links. The owner-only POST management route resolves its customer from the workspace, rechecks authorization after the provider call, and keeps ephemeral URLs out of persistence. The optional private management configuration enables a customer billing button; absent configuration keeps it hidden.

Tests cover provider field pinning, identity/link rejection, owner/developer/foreign access, logout during provider lookup, missing CSRF and injected customer fields. The full race-enabled integration suite, vet and builds passed; JavaScript syntax passed. Additional HTTP protection tests were run separately. No real provider session, cancellation, charge or live deployment was used. Rendered management-button and actual Stripe portal configuration tests remain part of sandbox qualification. Paid activation, refunds/disputes, merchant commerce, domain resale and Node isolation remain active requirements.

## Verified customer portal feature settings

Management sessions now validate the actual pinned Stripe portal configuration before creation and validate its expanded copy in the session response. Required settings include test mode, an active platform configuration, no public login page, subscription updates disabled, payment-method updates and invoice history enabled, and cancellation at period end with no prorations. This closes the earlier gap where only a configuration ID was checked while provider settings could expose unsupported subscription changes.

Tests reject live/inactive/incomplete settings, public login, subscription edits, immediate cancellation and settings changed between lookup and session creation. No provider configuration is created or modified. Sandbox feature verification and rendered management navigation remain outstanding; no real credentials, sessions, charges or deployments were used.

Validation passed: full race-enabled integration suite, vet and command builds; the additional configuration-race case also passed with race detection.

## Charge-to-subscription payment observations

Added a test-provider lookup for a captured successful charge and its InvoicePayment mapping. It validates charge/customer/PaymentIntent identity, platform scope, currency and amount bounds, then requires exactly one fully allocated invoice payment whose expanded invoice belongs to that customer and references a subscription. The request is bounded to thirty seconds and a single two-item list page; multiple/truncated allocations are rejected for later accounting handling.

The observation retains the captured amount, refunded amount and disputed flag alongside charge, invoice, customer and subscription IDs. It neither issues refunds nor grants or suspends hosting. Durable mapping storage, refund/dispute webhook handling and policy integration are still required.

Local fake-provider tests cover paid charges, partial refunds, disputes, live/uncaptured charges, invalid refund totals, split allocations, foreign payment/customer identities, unexpanded/standalone invoices and multiple/truncated mappings. No real Stripe requests, charges, refunds or live deployment changes were made.

Validation passed: full race-enabled integration suite, vet, all command builds and whitespace checks.

## Durable charge observations

Schema 13 adds charge observations with durable reconciliation generations and immutable subscription/customer/invoice/PaymentIntent identity. Trusted provider lookups must match a saved subscription customer, valid amount bounds and the current observation window. Later-started lookups fence older results. Once a charge is bound, a later response cannot move it to another invoice, payment or subscription. Failed checks preserve the previous timestamped observation.

Owner-only store reads enforce the workspace through its saved subscription. The records survive database reopen. These methods are not customer mutation routes and do not grant/suspend hosting or issue refunds. Refund/dispute event scheduling and freshness/access policy remain next.

Tests cover identity drift, unknown subscriptions, stale responses, concurrent lookup completion order, owner/developer/foreign boundaries and persistence of refund/dispute evidence after reopen. The existing old-schema migration fixture was updated to cover schema 13. A consistent pre-upgrade database backup is required for older-binary rollback; no live database was migrated.

Validation passed: full race-enabled integration suite, vet, all command builds and whitespace checks. No live requests, refunds or deployments were made.

## Refund and dispute event worker integration

The worker now discovers charge.refunded and dispute created/updated/closed/funds-withdrawn/funds-reinstated events. Verified object type and test mode are required. The event supplies only the charge lookup identity; current refunded amounts, dispute flags and ownership are retrieved from the authenticated provider and stored through the existing fenced reconciliation method.

After a successful charge lookup, an atomic transaction consumes the receipt, invalidates the linked subscription observation and schedules a fresh subscription read. Failed provider lookups remain pending. Already-processed receipts do not call the provider again. Charge persistence precedes receipt consumption, allowing safe replay after interruption. These changes neither issue refunds nor apply paid-access decisions. Pending risk events and the provider's dispute-history semantics must be considered by future access policy.

Tests send each supported event through signature verification, worker discovery, provider lookup and durable charge storage. They cover failed lookup retry, event payload values differing from current provider state, duplicate receipt handling and subscription refresh scheduling. All provider behavior is synthetic; no live credentials, refunds or deployments were used.

Validation passed: full race-enabled integration suite, vet, all command builds and whitespace checks.

## Current dispute outcomes

Charge retrieval now queries disputes for the exact charge and validates every returned dispute's charge, PaymentIntent, currency, test mode, amount and supported status. It retains dispute IDs, outcomes and amounts plus an explicit DisputesChecked marker. This distinguishes an older observation with no outcome data from a newly verified empty dispute list. A disputed charge without a returned dispute, duplicate IDs, foreign identities, unknown statuses and truncated results are rejected.

The lookup covers warnings, open/review states, won/lost and prevented disputes. The charge's historical disputed flag is still retained but cannot alone determine hosting access. Existing JSON snapshot storage preserves the additional evidence without a schema migration. Tests cover outcome validation and restart persistence; automatic access policy and periodic charge refresh remain unfinished. No real provider requests, disputes or deployments were used.

Validation passed: full race-enabled integration suite, vet and command builds; the extended outcome-persistence test also passed with race detection.

## Periodic charge refresh

Schema 14 adds durable refresh scheduling to charge records. The billing worker now checks one due charge alongside plan refresh and task processing. Successful observations schedule another check after five minutes; reservations/backoff last sixty seconds after failed or interrupted attempts. Existing reconciliation generations continue to prevent older lookup results from overwriting newer evidence. Scheduling persists across worker/store restarts.

Verified charge.succeeded events now also enter charge reconciliation, allowing known payments to be tracked before their first refund or dispute event. Early events remain retryable until invoice/subscription binding is available. Periodic refresh recovers missed changes for known charges; discovery of entirely unknown charges is still required for a complete provider reconciliation sweep.

Tests cover persisted schedules, failed refresh/backoff, later successful refresh and successful-charge event discovery in addition to existing identity/race tests. Schema 14 requires a consistent pre-upgrade backup for older-binary rollback. No live database, provider request or deployment was used.

Validation passed: full race-enabled integration suite, vet, all command builds and whitespace checks.

## Discover latest-invoice charges without charge webhooks

The Stripe adapter can now discover a charge from a subscription's latest invoice through expanded InvoicePayment and PaymentIntent records. It verifies the requested invoice/customer/subscription relation, test mode, matching currency and allocation amount, successful PaymentIntent and charge identity. Empty payment lists yield no charge, not paid entitlement. Multiple or truncated allocations remain unsupported.

Subscription reconciliation uses this discovery capability when supplied by the provider and a latest invoice exists. It atomically saves the fenced subscription observation and seeds a discovered charge for the existing periodic charge verifier. Existing charge records are not replaced or rebound by discovery. Provider discovery failures prevent a successful subscription observation update.

Tests cover valid/missing payments, multiple/truncated results, foreign identities, unexpanded PaymentIntents, live objects and queueing a discovered charge without a charge webhook. This recovers missing charge events for the current invoice of known subscriptions. A historical invoice sweep and missing subscription/customer identity recovery remain outstanding. No actual Stripe requests or live deployment changes were made.

Validation passed: full race-enabled integration suite, vet, all command builds and whitespace checks.

## Domain resale provider discovery and quote rules

Reviewed official NameSilo and Openprovider material. NameSilo is the initial budget-conscious candidate, pending sandbox access, exact operation contracts and resale-term/funding verification. No account, membership, support message or purchase was created. Source links and the registration/renewal implementation sequence are recorded in domain-resale.md.

Added provider-independent domain purchase validation and quote rules for the first .com/.net/.org ASCII product. URLs, subdomains, IDNs and unsupported names are rejected. Quotes require explicit non-premium classification, availability, registration and renewal prices, supported currency and a five-minute freshness window. Retail amounts use integer minor units with checked markup arithmetic. Tests cover name normalization, injection-shaped input, invalid names, stale/future quotes, missing renewal data, premium uncertainty and overflow.

This is the start of the domain implementation, not a registrar integration or purchase UI. Sandbox transport, owner-authorized order storage, payment coordination, registration/renewal lifecycle and qualification remain outstanding. Existing deployments and billing code were unchanged.

### Saved owner-only domain quotes

- Added schema 15 for immutable domain quote snapshots, retaining private registrar
  evidence and exact retail registration/renewal prices, currency and expiry.
- Rechecks owner authorization after provider access and freshness before persistence;
  reads enforce workspace ownership and evaluate expiry without altering the offer.
- Bounds retained quotes to 1,000 per workspace, checked before provider calls and
  transactionally before saving. Retention and HTTP rate limits remain launch work.
- Integration coverage checks restart persistence, workspace/developer denial,
  session revocation during provider access, expiry, storage quota and invalid
  registrar evidence. No live provider, purchases or deployment changes.
- Validation passed: full integration suite with the race detector, integration
  vet, all command builds and whitespace checks.

### Authenticated domain quote API

- Added opt-in POST/GET quote routes behind existing session, origin/CSRF and
  workspace-owner protection. Fixed markup comes exclusively from operator options.
- Added a bounded account-based lookup limiter across sessions/workspaces and
  generic provider-error responses. Domain quoting remains disabled by default;
  no purchase route, live registrar configuration or deployment changes.
- HTTP integration tests cover request tampering, foreign access, saved-offer reads,
  price injection, disabled configuration, provider-error redaction, cross-session
  throttling and rate-window expiry. Registrar transport and panel form remain.
- Validation passed: full integration suite with race detection, integration vet,
  all command builds and whitespace checks.

### Customer domain quote form

- Added an owner-only domain form, enabled by the server capability, showing exact
  retail registration cost, estimated renewal and expiry. Workspace changes and
  sign-out clear results; late responses from earlier workspace selections are ignored.
- Browser preview against a synthetic provider verified EUR prices, invalid-name
  feedback and sign-out. No external registrar requests or purchases occurred.
- Official provider research narrowed the remaining contract gap: sandbox endpoint,
  real availability/premium response shape and account pricing qualification are
  still needed before wiring NameSilo. No speculative registrar adapter was added.
- Validation passed: portal integration tests with race detection, customer portal
  build, JavaScript syntax and whitespace checks. The temporary preview process was
  intentionally stopped after browser checks, then removed before regression tests.

### Node build preflight

- Added immutable source-bound Node build specifications and a command-line
  preflight tool. Plans require assigned Linux amd64/arm64 architecture, target
  Node 24, support optional build scripts and define install/prune/start commands.
- Uploads are revalidated and their expected SHA-256 must match. No archive
  extraction, npm install, hooks or source execution occurs during preflight.
- Unit tests cover source mismatch, unsupported architecture/package manager,
  cancellation, optional builds and untrusted script contents. A disposable CLI
  fixture verified generated JSON and rejection of a mismatched source digest.
- Node executor, VM/network isolation, jobs, artifacts, secrets and runtime
  publication remain outstanding. Existing control-plane VMs and live nodes were
  not started or modified. See docs/node-builds.md for execution requirements.
- Validation passed: nodebuild unit tests with race detection, integration vet,
  all command builds, CLI fixture checks and whitespace checks.

### Private Node source extraction

- Added a source extraction stage bound to the assigned upload digest, using a
  private job root and confined filesystem handles. Each extraction gets a fresh
  directory; only owner permissions and necessary executable bits are retained.
- Revalidates ZIP protections before writing. Cancellation and errors clean up
  partial output; successful-directory and crash-orphan cleanup remain worker
  responsibilities. No npm commands or uploaded scripts are executed.
- Disposable filesystem integration tests pass with race detection for identity,
  private permissions, independent directories, nested executable helpers,
  traversal/symlinks and cancellation cleanup. Integration vet, command builds
  and whitespace checks also passed. No live deployments were changed.

### Durable Node build requests

- Schema 16 retains Node build plans, exact upload identity and operator-assigned
  toolchain digests. One pending build per project, idempotent request keys and a
  100-record history bound prevent duplicate dispatch requests and unbounded history.
- Current owner/developer authorization applies to request/list/cancel. Foreign
  project uploads and viewer/outsider access are rejected. Queued cancellation is
  idempotent; running work is rejected until an executor cancellation protocol exists.
- Source uploads referenced by builds are protected from deletion, including after
  cancellation. Recovery-oriented history/retention cleanup is still launch work.
- Integration tests cover concurrent retries, conflicting plans, foreign uploads,
  roles, retention, cancellation and restart persistence of a pending specification.
  HTTP routes, worker claims/execution and runtime activation remain outstanding.
- Validation passed: full integration suite with race detection, integration vet,
  all command builds and whitespace checks. Live nodes and databases were unchanged.

### Node worker claims and leases

- Schema 17 adds durable execution identity and hashed one-minute leases. Claims
  require matching project/toolchain/architecture, current submitter permissions
  and a verified source digest. Source bytes/raw leases are excluded from JSON.
- Renewals require the correct unexpired lease and current authorization. Expired
  running jobs cannot be reclaimed; their pending state and execution identity
  survive restart until the future VM reconciliation path establishes the outcome.
- Integration coverage checks mismatched targets, changed source bytes, revoked
  permissions, token rejection, renewal, expiry/restart and one winner among
  concurrent workers. Actual VM dispatch and completion remain outstanding.
- Validation passed: full integration suite with race detection, integration vet,
  all command builds and whitespace checks. Live deployments remain unchanged.

### Interrupted Node build failure recovery

- Schema 18 retains fresh, identity-bound executor evidence for failed/cancelled
  executions. Recovery requires an expired lease and explicit retirement of the
  entire operation, including delayed creates/restarts; a missing VM is insufficient.
- Terminal state and lease removal are atomic. Provider errors, stale/mismatched
  evidence, active leases and unqualified success leave the pending build intact.
  Recovery remains possible after account revocation and is idempotent after commit.
- Integration tests cover evidence rejection, provider failure, replay, revocation,
  restart persistence, stale lease rejection and a new explicit build after recovery.
- Inspected local Lima: the existing control-plane, restore and worker VMs were all
  stopped. No VM or live deployment was started/changed. Executor retirement still
  needs real provider implementation and qualification; tests use synthetic evidence.
- Validation passed: full integration suite with race detection, integration vet,
  command builds and whitespace checks.

### Real Linux Node build rehearsal

- Created a separate pinned ARM64 Lima VM and exercised the production plan/source
  extractor with a trusted synthetic Node app. Node v24.20.0 archive checksum was
  verified; bundled npm 11.19.0 ran as an unprivileged guest user.
- Passed install-hook, build output, offline prune, HTTP readiness, disabled prestart,
  process-group/listener shutdown and broken-build checks. Fixed the discovered
  npm issue where user/global configuration cannot share the same /dev/null path.
- Checked in the VM definition, fixture, source/plan helper and guest harness.
  The new VM was stopped after the rehearsal; existing lab VMs and live VPS/Pis
  were untouched. Temporary Mac inputs were removed.
- This is trusted-fixture execution evidence, not untrusted sandbox qualification,
  real worker dispatch or Node publication. Limits are in docs/node-build-rehearsal.md.
- Post-rehearsal checks passed: nodebuild integration tests with race detection,
  helper vet/build, Python syntax and whitespace checks. Lima confirmed all four
  local VMs stopped after cleanup.

### Linux service resource and network probes

- Added and ran trusted negative probes in the disposable Node VM. Verified
  cgroup memory/swap/PID/CPU settings, actual CPU throttling, OOM termination,
  timeout child cleanup, blocked network/elevation/system writes, and bounded
  job/temporary filesystems. Final probes passed.
- Fixed evidence collection after transient-unit cleanup and a real mount conflict
  where PrivateTmp defeated intended temporary-storage caps. The final profile
  uses explicit bounded private tmpfs mounts.
- Stopped/reset all probe services and stopped the lab VM. Live VPS/Pis and the
  other local labs were unchanged. Full scope and remaining qualification limits
  are recorded in docs/node-build-rehearsal.md; this is not public Node readiness.

### Restricted positive Node build

- Ran the synthetic Node fixture under the same memory/CPU/PID/time/filesystem/
  network/privilege restrictions used by negative probes, without raising limits.
  Install/build/prune, HTTP startup, shutdown and broken-build checks passed.
- Extracted a shared service profile and added deterministic Mac lab setup plus
  a guest positive runner. Fixed rehearsal reproducibility after VM reboot removed
  old /tmp inputs; pinned fixture/toolchain inputs now live read-only under /opt.
- Reran negative probes with the shared profile. Only the named disposable Node VM
  was used. Third-party package acquisition and production worker/runtime wiring
  remain outstanding; this result covers a trusted dependency-free fixture.
- Positive and negative live lab runs, Python syntax and whitespace checks passed.
  No test services remained active; all four local VMs were confirmed stopped.

### Registry dependency download validation

- Added source-bound modern npm lockfile download planning, URL deduplication and
  SHA-512-pinned public-registry tarballs. Rejects alternate sources, local links,
  weak/missing/conflicting integrity and root shrinkwrap precedence.
- The HTTP downloader disables proxies/redirects/content decoding, enforces time
  and per-tarball byte bounds, and returns only verified bytes with generic errors.
  It does not run npm, unpack packages or execute scripts.
- Synthetic ZIP/fake-transport tests passed with race detection, along with
  integration vet, command builds and whitespace checks. No VM/live deployment
  changes. Offline cache import, aggregate quotas, fetch isolation and worker
  integration remain; see docs/node-dependencies.md.

### Verified offline npm dependency rehearsal

- Added a fixture pinned to is-number 7.0.0 and a lab prefetch helper using the
  production source/digest/download checks. Package bytes are verified before
  transfer and again before importing into a fresh job-local npm cache.
- The restricted VM first rejected installation with an empty offline cache, then
  passed cache import, installation, build and HTTP execution of the real package
  with outbound networking disabled. Shutdown/broken-build checks also passed.
- The original dependency-free scenario still passes. npmfetch race tests, helper
  vet/build and Python syntax checks passed; whitespace was corrected. No package
  scripts ran on the Mac. The lab VM was stopped and live deployments untouched.
- Production cache/artifact storage, fetch isolation, transitive/native dependency
  qualification and worker/runtime integration remain. Details: docs/node-dependencies.md.

### Reusable dependency bundle store

- Added source-bound private bundle storage with a 100 MiB total byte cap, separate
  per-tarball bounds, sequential downloads, integrity rechecks and a synced final
  manifest containing source/content identity. Manifest digest is returned to the
  future trusted worker; no package code is unpacked/executed by this stage.
- Failure/cancellation removes partial output. Reopen/private-permission and
  provider/integrity/quota/cancellation tests passed with race detection. Successful
  retention and process-crash orphan reconciliation remain worker responsibilities.
- Replaced the lab-only writer with this shared component and reran the real
  dependency fixture successfully under the restricted offline VM profile. The
  lab was stopped afterward; live VPS/Pis were unchanged.
- Integration vet, command builds, Python syntax and whitespace checks passed.

### Dependency bundle reopen verification

- Added a bounded reader that checks trusted source/manifest identities, every
  tarball's filename/content hashes and size, private permissions and exact files.
  Incomplete, altered, extra-file and symlink bundles are rejected before import.
- The lab prefetch helper now uses this verifier before handing off a completed
  bundle. Trusted build-record binding, immutable consumption and orphan recovery
  remain worker integration work.
- npmfetch integration tests passed with race detection, plus integration vet,
  command builds and whitespace checks. No VM or live deployment was changed.

### Build-bound dependency bundles

- Schema 19 saves an immutable bundle directory/manifest digest on the exact Node
  build execution. Binding requires a valid worker lease and current submitter
  permissions, verifies files and matches the source lockfile's dependency set.
- Lease/permission checks repeat after filesystem work. Identical retries are
  idempotent; replacement, corruption, revoked access, expiry during verification
  and internally valid bundles with the wrong dependency set are rejected.
- Integration tests cover those cases plus persistent readback across restart and
  rejection of another execution's access. Stale execution cleanup, cache import
  wiring and VM dispatch remain. No VM or live deployment was changed.
- Validation passed: full integration suite with race detection, integration vet,
  command builds and whitespace checks.

## Node worker preparation (2026-09-10)

Connected build claiming, dependency download and immutable bundle binding in
`PrepareNodeBuild`. Background lease renewal cancels work on authorization loss;
a final renewal refreshes the executor handoff. Errors retain execution identity
and completed bundles for reconciliation, without making running jobs reclaimable.
Reviewed and fixed a cancellation race at successful handoff: renewal now finishes
before its context is cancelled.

Validation: integration coverage for the connected path, lease renewal,
revocation/provider errors, partial cleanup and no duplicate dispatch. This stage
still does not dispatch a VM or execute/publish Node applications. Production Node
execution, domain provider integration and merchant commerce remain outstanding.

## Durable Node execution handoff (2026-09-10)

Added schema 20 and worker-only `DispatchNodeBuild`. The handoff revalidates bound
inputs and authorization, then commits an immutable dispatch intent before its
single executor submission attempt. A lost response or worker restart cannot
resubmit the same build. The request carries an execution deadline and immutable
identities, without worker lease secrets. The executor must independently enforce
isolation, deadline and durable retirement; a nil submission response does not
complete or publish a build.

Integration coverage includes concurrent dispatch, uncertain response/restart,
prepared-build migration and rejected invalid/revoked/corrupted inputs. Production
VM execution and artifact publication remain outstanding; no live fleet was changed.

## Connected Node worker VM rehearsal (2026-09-10)

Qualified the real portal worker path against the trusted offline dependency
fixture inside the disposable ARM64 Linux VM: registration, upload, queued build,
claim/download/bind, durable dispatch, restricted npm install/build, HTTP readiness
and shutdown. Verified the guest rejects a repeated execution ID and the portal
rejects redispatch after reopening its database. The fixture executor checks exact
source/plan/toolchain/bundle correspondence with the staged service inputs.

The successful run reported one executor call and both duplicate guards passing.
Cross-compilation, Go vet and qualification-package checks passed. This provides
connected runtime evidence, not production executor completion. Per-build VM
provisioning, arbitrary verified input transfer, hard deadline/recovery behavior
and artifacts remain to be implemented and qualified. Live VPS/Pi deployments
were untouched. See `node-build-rehearsal.md` for reproduction and limitations.

## Node release artifact staging (2026-09-10)

Added bounded Node artifact validation and private extraction, including production
module files and internal npm command links. Digests/ZIP checksums are checked;
unsafe paths, special files, escaping/cyclic/dangling links, configuration secrets
and excessive sizes are rejected. Extraction strips unsafe permissions, syncs
completed files/directories, removes partial releases on error and preserves old
release directories.

The connected Linux worker fixture now exports its built/pruned application,
stages it through the new validator and starts that extracted release. Execution
`9a3fae863955ae88e4966e896fea021b43f4a383b0229b5c0493b65a0cd17353`
reported artifact staging, HTTP health, shutdown and both duplicate guards passing.
Full race-enabled integration tests, vet, command builds and Linux cross-build
passed. No live deployment changed. Production export after proven builder
termination, durable release records, storage/transfer, activation and rollback
remain outstanding. See `node-artifacts.md` for the format and limits.

## Persistent Node release records (2026-09-10)

Added schema 21 with bounded, immutable per-build archive retention. Recovery
requires fresh executor evidence matching execution/source/toolchain/architecture,
dispatched dependency manifest, successful retirement and artifact digest. Archive
validation, release insertion and build completion are atomic. Valid output from
a revoked submitter is discarded with a cancelled build and retained audit evidence.

Added current-owner/developer listing, integrity-checked reading and audited
idempotent deletion, with 10 releases per project and 500 MiB per workspace limits.
Tests cover persistence/migration, expired leases, evidence rejection, permissions,
revocation, concurrent retries, quota recovery and stored corruption. This is
retention only: no public routes, runtime activation or live fleet changes.

## Node deployment queue and release retention (2026-09-10)

Added schema 22 with idempotent deployment requests against exact retained releases,
operator runtime assignments and increasing project revisions. One pending request
per project is enforced in the database. Pending deployment references protect
archives from deletion; cancelling queued work frees only its reference and retains
history. Selecting older releases uses the same revision path for future rollback.

Workers can claim only matching runtime/toolchain/architecture assignments after
current permission and archive-integrity checks. Claims have unique operation IDs
and renewable leases; expired running operations cannot be reclaimed or cancelled
without runtime evidence. Tests cover request conflicts, retention, restart/history,
corruption, assignment mismatch, permissions, lease expiry and schema-21 migration.
No activation or public routing was added, and no live deployment changed. Health-
checked runtime activation, previous-release preservation and reconciliation remain
next. See `node-deployments.md` for contracts and remaining requirements.

## Persistent health-checked Node routing (2026-09-10)

Added `internal/noderouter` for a dedicated runtime's content route. It pins
operator-assigned loopback backends, persists the newest activation request before
probing, and changes the active route only after a successful HTTP health check
and transactional revision recheck. Failed/pending candidates leave the old route
serving; rollback uses a new revision. A late healthy candidate cannot overwrite
a newer route, and the active selection survives reopening the routing database.

Real HTTP-backend/TLS-frontend integration tests passed, including serving during
pending probes, failure preservation, rollback/restart, cancellation recovery,
redirect rejection and host/configuration checks. Full race-enabled integration
tests, vet and builds passed. This is the routing core, not a provisioner or public
runtime service: launcher identity/isolation, draining, authenticated transport and
portal activation reconciliation remain. Live deployments were not changed.

## Node runtime activation reconciliation (2026-09-10)

Added schema 23 with persisted runtime evidence and active deployment pointers.
Reconciliation requires fresh exact operation/revision/release/runtime identity,
actual toolchain/architecture, settlement and current health for success. Failed
candidates must be stopped and preserve the previously recorded active route.
Completion and reference changes are atomic; active archives have an independent
restricting foreign key. Silent cross-runtime moves are rejected.

Recovery works after lease expiry/restart and records actual serving facts after
submitter revocation with an explicit audit flag. It never initiates deployment.
Connected real-router/HTTP tests and contract tests cover success/failure, stopped
candidates, identity/evidence errors, reference cleanup, tenant access, idempotency,
revocation and migration of an outstanding schema-22 operation. Production launcher,
authenticated runtime transport, worker orchestration and qualification remain.
No live VPS/Pi deployment was changed.

## Authenticated Node runtime observations (2026-09-10)

Added a scoped HTTPS status handler and client implementing `NodeRuntimeReader`.
Access requires the assigned management host, bearer token, project and runtime;
browser requests, bodies, queries and mutations are denied. Fresh nonce envelopes
prevent cached response replay. The client verifies certificates, disables proxy/
redirect/decompression inheritance and rejects oversized or invalid responses.
Provider calls are bounded and concurrent reads capped.

TLS security tests and a connected portal/reconciliation integration test passed
with explicit executor/runtime fixtures. No production launcher is implied by these
fixtures. The API exposes only read-only observations; production process identity,
listener provisioning, activation transport and worker orchestration remain.
Live deployments were unchanged. See `node-runtime-api.md` for the contract.

## Restricted Node runtime service profile (2026-09-10)

Added a validated service renderer with dedicated non-root identities, read-only
releases, bounded temporary storage, resource limits, loopback-only IP policy,
control-group shutdown and limited crash restarts. A trusted synthetic fixture in
the disposable ARM64 VM verified the profile, successful HTTP serving, two crash
recoveries, restart exhaustion and listener removal. The VM was stopped afterward.
Go race-enabled integration tests, vet and builds passed.

This is a renderer and rehearsal, not the production launcher. Durable identity
reservations, verified installation, activation transport, routing integration and
hostile runtime/recovery qualification remain. Loopback access is not tenant
network isolation. See `node-runtime-services.md` for evidence and caller contracts.
The live fleet and customer databases were unchanged.

## Durable Node runtime reservations (2026-09-10)

Added a private runtime pool database with immutable operation assignments,
exclusive UID/port slots, single start claims and permanent retirement tombstones.
Slots remain occupied until a trusted fresh observation proves delayed starts
fenced, all processes stopped, listener removed and routing references detached.
The accepted evidence is retained with retirement. Startup inspection lists all
outstanding reservations without authorizing repeated starts.

Tests passed for competing database handles, process exit without database close,
reopen/retry, conflicts, stale or incomplete retirement evidence and slot reuse
without resurrecting old operations. Full race-enabled integration tests, vet and
builds passed. Production service-manager fencing, actual account/port checks and
connected lifecycle qualification remain. No live deployments changed.

## Verified read-only Node release installation (2026-09-10)

Added a Linux/root-only installer that validates archive identity, privately
extracts and seals root-owned files, then publishes a fresh release directory with
an atomic no-replace rename. It syncs files and publication parents, removes
pre-publication staging on failure and preserves uncertain published results for
reconciliation. It executes no customer scripts.

Real Linux-root tests passed archive rejection, permissions, internal symlinks,
cleanup and unprivileged read/write checks. The service rehearsal now installs its
fixture ZIP through this code before starting Node; resource/network restrictions,
crash restarts, restart exhaustion and listener shutdown passed again. The VM was
stopped after testing. Reservation-to-installation binding, service-manager
fencing, toolchain installation and recovery qualification remain. No live fleet
or customer database changes were made.

## Node installation receipts and start authorization (2026-09-10)

Pool schema 2 records an installation attempt before dispatch and retains the
verified installation receipt. Starting now requires the matching receipt.
Uncertain/failed attempts cannot be automatically repeated, and late installation
results cannot make retiring operations startable. Release directory aliases
across operations are rejected. Existing schema-1 starts retain their state and
cannot be installed or started again after migration.

Race-enabled tests covered concurrent preparation, process exit inside the
installer, receipt persistence, incorrect evidence, retirement races and migration.
A real Linux-root test connected reservation, read-only installation, receipt and
start authorization, including refusal to replace an existing release. The test
VM was stopped afterward. Service-manager dispatch/fencing, unknown-installation
reconciliation and the remaining full MVP requirements are still open. No live
deployment or customer database changed.

## Recovery of unacknowledged Node installations (2026-09-10)

Added complete sealed-tree verification against the retained archive and trusted
Linux installation observations. Recovery checks all paths, bytes, modes, symlink
targets, root ownership and hard-link counts, then syncs the tree and persists the
receipt only while the operation remains reserved. It never reinstalls files or
undoes retirement. Cached or mismatched observations cannot authorize a start.

A real Linux child exited after release publication but before receipt storage.
Reopening and verification recovered the receipt without a duplicate installation,
after which start authorization succeeded. Drift, ownership/link errors, stale
evidence and retirement races were rejected. Service-manager dispatch/fencing,
toolchain lifecycle and full hosting/pilot qualification remain. No live fleet or
customer database changed; the disposable VM was stopped after testing.

## Durable Linux service control gate (2026-09-10)

Added per-operation cross-process locking and durable install/start/retirement
records. Intent commits precede host actions. Failed cleanup retains a permanent
retirement fence; delayed starts and installations remain blocked. Pool preparation
and installation recovery can use the same gate through `GatedInstaller`.

Linux tests passed contention, cancellation, process exit, failed/retried cleanup,
retirement-before-install and record validation. Symlink regression tests exposed
and verified a fix using direct kernel no-follow opens. The actual Node service
rehearsal passed start, crash recovery, stop and rejection of a late start through
the gate. The production service manager, toolchain/account checks, routing and
full recovery remain incomplete. No live deployments changed; the disposable VM
was stopped and its gate records retained.

## Exact local Node service status (2026-09-10)

Added bounded, read-only systemd status inspection for the assigned unit. Strict
property parsing rejects incomplete identity/path/PID data; stopped status requires
no main/control process, queued job or pending reload. The actual VM rehearsal
verified the running and stopped service through this inspector, while its crash
recovery and late-start fence checks continued to pass.

This is unit metadata, not complete retirement proof. UID/cgroup and listener
checks, routing detach/drain and production service-manager integration remain.
No live deployments changed; the disposable VM was stopped after qualification.

## Linux runtime usage observations (2026-09-10)

Added independent checks for matching UID processes, service cgroup population and
IPv4/IPv6 TCP listeners. Unsupported or unreadable kernel data fails observation.
Real Linux tests detected a process outside the service cgroup and a listener
owned by another user. The actual Node rehearsal observed usage while running and
its absence after shutdown, alongside the existing service-status and late-start
checks.

These observations do not yet authorize slot reuse: namespace coverage, restart
prevention, routing detach/drain and the final retirement adapter remain. No live
deployments changed; the disposable VM was stopped after testing.

## Permanent Node routing retirement fences (2026-09-10)

Routing schema 2 persists immutable retirement records. Activation checks them
before and after health probing, so a delayed result or retry cannot resurrect a
retired operation. Initial retirement refuses active operations/backends and keeps
the replacement route serving. Pending retired candidates become failed without
changing the active route.

Real HTTP tests passed a retirement racing a blocked health probe across database
connections, restart persistence, active-route protection, replacement serving,
new-operation backend reuse and migration preserving a schema-1 active route.
In-flight request draining and complete retirement integration remain. No live
deployment or customer database changed, and no VM was needed for this step.

## Exclusive Node upstream ownership (2026-09-10)

Added lazy kernel-lock ownership for proxy traffic and health activations. Other
control handles can inspect and fence retirement, but cannot serve or probe.
Closing the owner denies new upstream work while retaining the lock until its
existing requests finish, preventing premature handover to a replacement router.
Private regular lock files are required; symlinks and hard links are rejected.

A real HTTP test held an old response open across route replacement and owner
close, verified that another handle remained excluded, then completed the response
and successfully handed over replacement traffic. Unsafe-lock regression tests
passed. Per-backend draining and the production retirement adapter remain open.
No live deployment or customer database changed.

## Guarded Node backend draining (2026-09-10)

Added per-backend request and health-operation tracking and a guarded retirement
action. The drain waits for old traffic, rechecks active/pending routes, and
excludes new activations while a synchronous stop completes. Separate content
read connections keep the replacement serving during this guard. Cancellation
cannot release the guard beneath an action still running; retries reject a backend
already reused by another active operation.

Real HTTP tests covered outstanding old requests, health probes, canceled waiting,
replacement serving during the action, activation exclusion after cancellation,
and reuse protection. Production launcher integration and complete retirement
qualification remain. No live deployment or customer database changed.

## Masked systemd retirement (2026-09-10)

Added a Linux service retirement adapter under the durable control gate. It
persistently masks the exact unit, syncs the mask directory, stops the service,
and verifies masked, settled systemd status. Uncertain command results preserve
the retirement fence and must not free reservations.

The real ARM64 Linux rehearsal resumed its trusted fixture after crash-limit
qualification so retirement stopped a live service. Process/cgroup/listener
clearance, repeated retirement, direct systemd start rejection and delayed gate
start rejection passed. Qualified operation:
`d42b879ac62549fdaeddb02c42972b308ff0d6682bf14295b117a3bc82d419cb`.
Full portable race tests, vet/build and Linux cross-vet passed. Complete routing
and reservation retirement integration remains. No live deployments changed.

## Routed retirement and slot release (2026-09-11)

Connected candidate/reservation identity checks, router drain, durable service
retirement and fresh kernel observations. Reservation release commits while the
routing guard remains held. Errors keep slots occupied; terminal retries do not
stop or release a replacement reservation. The controller rejects namespaces that
differ from its visible PID 1; dedicated-host provisioning remains required.

Real Linux integration verified mismatched identity rejection, an occupied TCP
listener preventing release, retry after listener removal, durable pool reopen,
slot reuse and old-operation retry safety while replacement HTTP kept serving.
The test uses an absent systemd service; live Node service stopping was qualified
separately. Full live deployment/retirement orchestration qualification remains.
No live deployment or customer database changed.

## Connected live Node runtime retirement (2026-09-11)

The Linux rehearsal now connects durable reservation, installed receipt, start
claim, real restricted Node/systemd execution, routing replacement and verified
retirement. It reopens the pool, reuses the slot and checks that retrying the old
operation leaves the new reservation and replacement HTTP response intact. The
unused replacement reservation is cleaned through the same retirement workflow.

Real ARM64 execution passed for operation
`c6c6e3a89eb74dac8e7fc8707e75a41a1d1d7db3a45b4a308dba7dd3973deb91`, including crash
recovery, resource/network restrictions, masked shutdown and late-start rejection.
The replacement is a synthetic HTTP service. Public upload/build/deployment
orchestration, toolchain/account provisioning and full customer pilot remain.
No live deployment or customer database changed.

## Durable customer deployment handoff (2026-09-11)

Added portal schema 24 and a one-attempt runtime dispatch method. It validates the
current lease, actor authority, release linkage and archive bytes before saving
an immutable, credential-free intent and submitting. Acceptance is not reported
as deployment success. Lost responses and concurrent retries cannot dispatch the
operation again. Migration fences legacy running work for reconciliation.

Race-enabled tests passed persisted intent before external submission, lost-reply
reopen, concurrent calls, invalid/expired leases, revoked users, corrupt archives
and legacy migration. The concrete runtime receiver, worker loop and customer
upload/build-to-deploy experience remain incomplete. No live deployments changed.

## Feature-first delivery and Node panel controls (2026-09-11)

Updated the delivery plan and 02:30 automation to prioritize usable features and
focused verification. Broader qualification is deferred until product flows work.

Added customer Node project status, build buttons, queued-operation cancellation,
saved release deployment/restore/delete controls, deployment history and active
website links. The HTTP layer chooses pinned runtime/build assignments from private
operator configuration; browsers cannot supply infrastructure targets. Workspace
roles and CSRF checks protect mutations. Viewers retain read-only upload listings.

Focused HTTP tests passed build/deploy/cancel flows, retry identity, unassigned
projects, cross-workspace denial, viewer mutation denial and CSRF. Static publication
checks, the customer portal build and frontend syntax checks passed. These controls
queue real stored operations; execution still needs the concrete runtime receiver
and build/deployment worker loop. No live deployment changed.

## Node deployment worker and HTTPS submission (2026-09-12)

Added a runnable assigned deployment worker and authenticated runtime submission
transport. The worker submits queued requests once and reconciles running work
without resubmitting. The runtime API verifies project/runtime credentials,
request identity/deadline and bounded archive bytes before provider acceptance.

Focused TLS tests passed successful transfer, wrong runtime, expired submission
and wrong credential rejection. A connected store/worker test passed accepted-
but-lost reply recovery without a duplicate submit and verified the active portal
pointer. Worker build and focused vet checks passed. The durable runtime receiver
that calls installation/start/routing remains the next missing execution layer.
No live deployment changed.

## Durable runtime deployment acceptance (2026-09-12)

Added the concrete runtime inbox submission provider. It persists validated
archive bytes and immutable operation metadata before acceptance, pins project,
runtime, architecture and toolchain, and prevents replays from launching twice.
Processing work survives restart for reconciliation rather than being reclaimed.

Focused tests passed durable accept/claim/reopen, settled-request replay, next
revision acceptance and invalid scope/deadline/archive/identity rejection. Runtime
execution and retired archive cleanup remain to be connected to this inbox.
No live deployment changed.
