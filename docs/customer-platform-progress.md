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

## Not implemented yet

This is not a production-ready public account service. Returned verification/reset tokens are intended only for trusted mail delivery, never browser responses. There is no HTTP signup endpoint or production exposure yet. The new HTTP service has per-address login limits and a bounded global request gate. Production proxy behavior, distributed abuse protection and operational qualification remain outstanding.

Next: durable mail outbox, signup/reset UI, verification delivery, invitations, owner bootstrap, administrator MFA/recovery, account administration and additional authorization tests. Workspaces and project metadata exist; project uploading, builds and publication do not.

Still outstanding: isolated HTML/Node upload/deployment pipeline and release recovery, Stripe hosting subscriptions, Connect merchant sales, registrar selection/domain resale, full pilot qualification, backup/restore drills and production rollout. See `customer-platform-plan.md` for the unchanged overall scope.

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
