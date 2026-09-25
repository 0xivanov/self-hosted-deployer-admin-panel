# GitHub deployment

Status: implementation started September 25. Repository archive preparation, push signature validation and GitHub App
authentication, OAuth exchange, repository access checks and session-bound connection persistence are source components, not a connected customer
feature. No GitHub webhook endpoint, installation callback or deployment worker
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
  limit error when the selection cannot be verified within that bound. This component still needs portal
  connection handlers before it is customer-accessible.

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

1. GitHub App configuration, identity/link flow, project-bound installation and
   repository selection, connection CRUD and visible disconnect controls.
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
redirects and does not retain returned credentials. HTTP routes, private operator
configuration, repository selection UI and an actual GitHub App remain outstanding.

Focused checks cover connection lifecycle/revision, callback replay, disconnect
and replacement races, role/session changes, expiry, database reopen and schema
47-to-48 migration. No production migration or feature enablement was performed.
