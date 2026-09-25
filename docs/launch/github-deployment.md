# GitHub deployment

Status: implementation started September 25. Repository archive preparation, push signature validation and GitHub App
authentication, OAuth exchange, repository access checks and session-bound connection persistence are source components, not a connected customer
feature. Customer connection routes and controls are now implemented locally. No GitHub webhook endpoint, connection flow or deployment worker
is enabled in production yet. Do not advertise deploy-on-push as available.

## Customer experience

Within a static or Node project, an owner connects GitHub, grants the Launchstead
GitHub App access to selected repositories, selects a repository and branch, and
optionally chooses a folder. The connection screen shows the exact repository,
branch, folder and who authorized automatic deployment. It offers an initial
Deploy and an explicit deploy-on-push setting. Disconnect stops future imports;
it does not delete the last working website.

New commits flow through the existing validated upload and static publication or
Node build/deployment queues. Show the commit, build/deployment stage, logs,
failure explanation and previous working release in the ordinary project view.
Failed or superseded commits must not replace a healthy site. Existing manual ZIP
uploads and retained-release restoration remain available.

This first path supports the current static/Node contracts. Static repositories
need an index.html in the selected folder; Node repositories need the supported
package.json/start script and package-lock.json. Dockerfile builds and arbitrary
GitHub Actions workflows are separate work. Do not execute Git on the control
plane or run repository hooks to obtain source files.

## Provider and authorization

Use a GitHub App with repository Contents read permission and selected repository
access. Installation tokens must be restricted to the selected repository with
read-only content access. The app private key and webhook secret stay in private
operator configuration. Do not reuse the operator's personal GitHub CLI token.
GitHub Apps support selected repositories and narrow permissions:
[GitHub App documentation](https://docs.github.com/en/apps/creating-github-apps/about-creating-github-apps/about-creating-github-apps).

A numeric installation ID in a callback is not proof that the signed-in portal
user controls the installation. The link flow must bind expiring, single-use
state to the portal user/project and verify the user's GitHub identity plus
installation/repository access before saving the connection. Recheck project
ownership, verified account, workspace access and hosting entitlement at commit.
Store the authorizing actor and connection revision, never a reusable portal
session token, for background work. Removed access, suspension, uninstallation,
project deletion or disconnect must prevent new imports/publications.

## Implemented source components

`internal/githubdeploy` includes:

- Provider-side verification of the GitHub user, matching App installation and
  readable repository. It resolves the numeric app ID through an app-authenticated `/app` request,
  then checks installation app identity, suspension and pull permission;
  a callback-supplied installation ID alone is insufficient. Requests stay on
  fixed GitHub API endpoints and use bounded pagination/responses without
  retaining the user token. Listings stop at 1,000 items and report an explicit
  limit error when the selection cannot be verified within that bound. Customer connection handlers now use these checks, but remain off in production.

- GitHub App RSA key validation and short-lived RS256 app assertions, plus a
  fixed-endpoint installation-token client. Every token request explicitly selects
  one numeric repository ID and contents-read permission. Responses must confirm
  that repository, read-only scope and a bounded expiry. Redirects are refused;
  credentials are excluded from JSON and ordinary diagnostic formatting. Operator
  private-file configuration and user/installation linking are still required.

- Raw-body HMAC-SHA256 push verification and bounded parsing. A verified event
  identifies an installation/repository/ref/commit; it does not grant portal
  access or trigger a deployment by itself.
- Repository ZIP preparation that strips one enclosing GitHub archive directory
  and optionally selects a repository folder without extracting to disk. It
  rejects multiple roots, unsafe paths, selected links/special files and oversized
  input/output. Prepared archives pass the existing upload validator, including
  source-secret exclusions, expanded limits and static/Node entry requirements.

The provider signature algorithm follows
[GitHub webhook validation](https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries).
Headers such as event type and delivery ID are not covered by the body signature;
they cannot substitute for body validation, authorization or durable deduplication.
Archive folder names do not authenticate a repository or commit. Obtain source
only through a verified installation and an exact commit resolved through GitHub.

## Next implementation slices

1. Register/configure the real GitHub App and complete the installation-access journey. Local private configuration, OAuth/link routes, repository selection, saved connections and disconnect controls are implemented; real provider use remains unverified.
2. Durable connection and import records. Authenticated webhook intake stores
   the signed payload hash, installation/repository identity, exact ref/commit
   and connection revision transactionally. Changed or replayed delivery headers
   must not create another deployment for an already accepted connection/commit.
   Return success only after durable intake; do not perform builds in the request.
3. Bounded archive fetching with short-lived installation tokens. Validate API
   endpoints and redirect destinations, never forward authorization to an
   arbitrary host, limit bytes and time, and fetch the exact commit. Feed the
   prepared archive into the existing immutable upload contract with current
   actor/hosting authorization and quotas rechecked.
4. Connect imports to static/Node queues, retaining provenance and a stable job
   identity through retries. Confirm the configured branch still selects that
   commit before publication; out-of-order events must not publish stale code.
   Force-pushes, branch deletion, revoked installation access and disconnect need
   explicit terminal states and customer-facing recovery guidance.
5. Customer UI and one real selected-repository publication on the worker fleet,
   including a push update and a failed update retaining the previous release.
   Then enable the feature in production and update onboarding/marketing.

The schema must be upgraded across all portal database consumers together, as
in the [September 25 coordinated upgrade](production-upgrade-20260925.md).


## App authentication evidence, September 25

App signing follows [GitHub's JWT requirements](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-json-web-token-jwt-for-a-github-app).
Installation requests use the [installation access-token endpoint](https://docs.github.com/en/rest/apps/apps#create-an-installation-access-token-for-an-app)
with explicit repository and permission restrictions. The client uses a dedicated
HTTPS transport without environment proxy settings, a 20-second request timeout,
a 1 MiB response limit and no redirect following. It does not cache tokens or
return upstream response bodies in errors.

Focused checks independently verified JWT signatures/claims and key rejection,
request scope, response scope/expiry rejection, redirect refusal and credential
redaction. These checks used generated keys and mock provider responses. No real
GitHub App has been registered/configured through this code, and no customer
repository access or deployment has been attempted.


## Local linking state (schema 47, not deployed)

The portal now supports beginning and consuming a GitHub link request for a
static/Node project. Both operations require current verified owner/session
access, a non-deleting project and hosting entitlement. State is random, hashed
at rest, expires after ten minutes, and is bound to the initiating session,
actor and project. One pending attempt per project is retained; a replacement
invalidates the old one and attempts are limited to one every ten seconds while
an attempt is pending. Consumption uses a conditional transactional delete, so
only one callback succeeds. Provider failure after consumption requires starting
a new link request. The raw state/session token is never stored or audited.

This is not a completed account connection. HTTP CSRF checks, OAuth code exchange and provider access verification
must be wired to customer-facing handlers. Connection persistence now rechecks
portal ownership and entitlement before saving. Provider checks follow the
[GitHub user installation/repository APIs](https://docs.github.com/en/rest/apps/installations).

Migration and focused checks preserved existing project/session data and covered
state persistence across reopen, replay, cross-user/session/project attempts,
expiry, role revocation and project deletion. Production remains schema 46;
all portal database consumers will need the next coordinated upgrade together.


## Connection persistence and OAuth (schema 48, not deployed)

Project connections retain the authorizing actor, GitHub user, installation and
repository IDs, repository name, branch, optional folder and explicit deploy-on-push
setting. They never retain reusable GitHub or portal tokens. Replacing or
disconnecting a connection advances its revision; future import workers must
check that revision before acting. Disconnect preserves the current published site.

After consuming callback state, the store issues a second short-lived single-use
completion receipt bound to the same session, actor and project. Only its hash is
stored. Starting a new link or disconnecting revokes unfinished receipts, so a
late provider response cannot reconnect a project. Saving rechecks current owner
access and hosting entitlement inside the transaction that consumes the receipt.

The OAuth helper follows [GitHub App user-token authorization](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app), creates authorization URLs with PKCE S256 and exchanges codes
through the fixed GitHub token endpoint. It bounds responses and time, refuses
redirects and does not retain returned credentials. HTTP routes, private operator configuration and repository selection UI are now implemented locally. An actual registered GitHub App and provider qualification remain outstanding.

Focused checks cover connection lifecycle/revision, callback replay, disconnect
and replacement races, role/session changes, expiry, database reopen and schema
47-to-48 migration. No production migration or feature enablement was performed.


## Customer connection flow (local, not deployed)

`customer-portal --github-config` accepts a private JSON file (at most 16 KiB)
with `client_id`, `private_key_pem` and `client_secret`. Configuration is rejected
in demo mode. The exact GitHub OAuth callback is the configured HTTPS portal
origin plus `/github/callback`. Do not enable this option in production until
repository import and publication are connected and verified.

When configured, static/Node project owners can authorize GitHub, choose an
accessible repository/branch/folder, view the saved connection, and disconnect.
Repository discovery is scoped to the configured App and current user. Connect
rechecks provider access before saving. Automatic deployment remains false;
the UI explicitly says GitHub publishing is not enabled and ZIP uploads still work.
The App must already have been installed for the selected repository; the guided
installation/access-management journey remains to be completed.

The callback landing page removes OAuth parameters from browser history, then
uses the authenticated same-origin CSRF-protected POST flow. This preserves the
portal's Strict session cookie. PKCE verifiers are derived from the session and
random state with HMAC; neither verifier nor raw session is persisted. Temporary
GitHub user tokens are held in a bounded in-memory selection flow for at most the
flow's ten-minute authorization window, are inaccessible after expiry, and are
removed on successful connection/disconnect or later cleanup. A portal restart requires reconnecting.
Refreshing or revisiting a callback cannot exchange its code twice. A late token
exchange cannot restore a disconnected or superseded selection.

Focused HTTP checks cover CSRF, session binding, replay, late-response rejection,
repository selection, disconnect and the no-cache callback page. These use fake
provider responses, not a real GitHub installation. Production remains schema 46.


The customer UI regression checks exercise pending repository selection, numeric
repository IDs, disconnected state, restart controls, callback workspace switching
and callback single use. Invalid branch/folder input preserves the temporary
selection so the owner can correct it without repeating OAuth. These are focused
DOM and HTTP checks; the full browser/provider journey remains unverified.


## Manual repository imports (local schema 49, not deployed)

The portal now has a durable project import queue and a background source
worker. A manual import resolves the selected branch once, persists its full
commit, downloads only that commit and normalizes the selected repository folder
through the existing archive validator. Prepared bytes become an ordinary immutable
upload. The customer then uses the existing Build/Publish workflow; importing alone
does not change the live website.

Import jobs bind the connection revision and authorizing user, use expiring worker
leases, and recheck current owner/hosting access and upload limits before saving.
Restart recovery retains the same pinned commit, with at most five worker claims
per job. Disconnect/reconnect or removed access prevents a stale worker from saving
its upload. Both the requesting owner and the connection authorizer must retain
verified owner access. Job errors use fixed safe reason codes, never raw provider
responses or signed download URLs.

This does not complete deploy-on-push: durable webhook intake, event deduplication,
branch ordering and automatic queue/publication integration remain outstanding.
Production is unchanged and no real GitHub App import has been attempted.


Focused backend checks passed for manual request deduplication, cross-user denial,
validated immutable upload creation, pinned-commit recovery and disconnect during
fetch. Import alone creates no publication. Migration checks preserve earlier
portal data. No customer source is executed by this worker; Node builds still use
the existing isolated worker path. All database consumers must be upgraded together
to schema 49 before production activation.


Archive fetching follows [GitHub's repository archive endpoint](https://docs.github.com/en/rest/repos/contents#download-a-repository-archive-zip).
Each fetch gets a repository-scoped installation token, verifies the numeric
repository identity, and requests a full commit SHA. A single redirect is allowed
only to the matching repository/commit path on HTTPS `codeload.github.com`; the
API authorization header is not forwarded. Signed redirect queries are bounded
and excluded from diagnostics. Downloads are limited to 10 MiB and timed out.
