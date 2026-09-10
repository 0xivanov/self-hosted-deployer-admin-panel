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

This package is not a public account service. Returned verification/reset tokens are intended only for trusted mail delivery, never browser responses. There is no HTTP signup endpoint or production exposure yet. Endpoint rate limiting and bounded request queues must precede public access; the password-hashing semaphore alone is not sufficient abuse protection.

Next: durable mail outbox, customer HTTP service, session cookies/CSRF/host boundary, signup/login/reset UI, verification delivery, invitations, owner bootstrap, administrator MFA/recovery, account administration and additional authorization tests. Workspaces and project metadata exist; project uploading, builds and publication do not.

Still outstanding: isolated HTML/Node upload/deployment pipeline and release recovery, Stripe hosting subscriptions, Connect merchant sales, registrar selection/domain resale, full pilot qualification, backup/restore drills and production rollout. See `customer-platform-plan.md` for the unchanged overall scope.
