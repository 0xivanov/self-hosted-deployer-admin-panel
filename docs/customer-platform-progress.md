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
