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

1. Register/configure the real GitHub App and verify the installation-access journey. Local private configuration, OAuth/link routes, repository selection, saved connections and disconnect controls are implemented; real provider use remains unverified.
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
The repository access guide opens the verified App installation page in a separate
tab. Customers select repositories on GitHub, return to the portal, and refresh
the chooser or continue authorization. GitHub installation IDs supplied in URLs
never establish portal ownership or grant repository access.

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

This does not complete deploy-on-push: durable webhook intake and event deduplication are now implemented locally.
Branch ordering, push processing and automatic queue/publication integration
remain outstanding.
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


## Guided repository access

The portal resolves the installation URL from authenticated `/app` metadata,
verifies it belongs to the configured App, and constructs the canonical GitHub
installation link from its validated slug. It ignores provider-supplied arbitrary
HTML URLs. Only a current project owner can request this setup link.

The guide is available before connection, when repositories are missing, and for
an existing connection. It opens GitHub in a separate tab with no opener/referrer,
explains selected-repository access and lets the owner refresh the chooser after
saving. It follows [GitHub's documented installation URL](https://docs.github.com/en/apps/using-github-apps/installing-a-github-app-from-a-third-party).
Focused provider, HTTP and DOM checks passed; actual App registration and a real
installation/import remain unverified. No production configuration changed.


## Durable push intake (local schema 50, not deployed)

The optional `webhook_secret` field in the private GitHub configuration enables
`POST /webhooks/github` over the configured HTTPS host with JSON bodies. Use a
random secret of at least 32 bytes. The endpoint authenticates the exact raw body,
limits it to 2 MiB and ignores unsigned event/delivery headers when classifying
or deduplicating events. Signed pings and unsupported event bodies are acknowledged
without changing projects. Malformed or unauthenticated requests are rejected.

A push is retained only for matching connected repository/branch bindings with
deploy-on-push enabled and current verified owner/hosting access. Each delivery
has a global payload receipt and separate project bindings, so one repository
can feed multiple sites atomically. The inbox deduplicates each project's signed payload identity. It allows separate
provider events to revisit the same branch before/after commits. It stores identity and commit
metadata, never the raw provider payload or credentials. Intake returns success
only after its database transaction commits.

Receipt history and per-project event history are capped at 10,000. Capacity
failures return a retryable error and roll back the entire intake, including its
receipt. A repeated already-retained payload still succeeds at capacity. One
request matches at most 1,000 project bindings. Retention will be added before enabling this endpoint in production.

Checks cover multiple matching projects, replay with changed unsigned headers,
wrong bindings, disconnect, provider ping, endpoint restrictions, capacity rollback,
and migration/reopen preservation. Push events feed the processor described below. Automatic publication is available behind the separate configuration described below. Existing manual GitHub imports and ZIP deployment behavior are unchanged.

Before automatic publication, the pipeline must recheck the connection and branch
head before entering publication. Node pipelines must repeat that check after
the build, since a newer commit can arrive while code is being built. Existing
runtime publication revisions and recovery remain authoritative. No browser session
or operator personal token should be manufactured for background deployment work.


## Push processing (local schema 51, not deployed)

When webhook configuration is present, the portal processes retained pushes before
its normal import pass. Processing has a two-minute lease and at most five claim
attempts. Provider errors and shutdown leave the lease for recovery; provider
error details are not persisted or exposed. The processor resolves the actual
branch head, skips superseded events and branch deletions, and atomically enqueues
one existing import job pinned to the verified commit. Each event has a stable
request key, so recovery cannot enqueue a second import for that event.

Claim and completion recheck the connected repository revision, branch, authorizing
owner, project lifecycle and hosting access. Existing queued/running imports and
history limits still apply. Import completion continues to validate source through
the existing archive path. A push import creates an upload. The separately enabled automatic pipeline below
can build and publish that upload through the existing workers.

Schema 51 adds processing records without rebuilding the schema 50 inbox. Upgrade
all portal database consumers together before activation. Production is still
schema 46 and has no GitHub App configuration. Actual GitHub installation and
provider delivery are not yet verified.

Schema 53 removes permanent semantic tuple deduplication so legitimate branch
return cycles can revisit the same before/after pair. Exact signed payload receipts
still suppress redelivery. Retention must preserve this distinction. Automatic publication must also serialize with manual publication
and recheck branch head after a Node build.

Focused integration checks passed for push-to-upload, provider-failure recovery,
stale leases, retry exhaustion, superseded/deleted branches, manual-import
contention, reconnect/disconnect fencing and schema migration with retained inbox
events. Portal and customer-portal static analysis passed. These are local checks,
not evidence of a real GitHub delivery or production publication.


## Automatic build and publication (local schema 52, not deployed)

`--github-auto-deploy` requires the private GitHub App, OAuth and webhook settings.
It exposes per-project automatic deployment controls and activity in the connected
repository panel. Enabling requires an assigned static origin or Node runtime.
The option is off by default and is not enabled in production. Manual imports and
ZIP workflows continue to work without it.

Successful push imports create durable pipelines. Static projects enqueue normal
publication jobs. Node projects enqueue normal isolated build jobs, then deploy
that build's retained release through the normal deployment worker. The pipeline
checks GitHub's branch head before enqueueing and again after a Node build. It
keeps stable request identities, tracks actual worker terminal results, and rotates
between projects so a slow build or provider failure cannot monopolize the queue.
There is one active automatic pipeline per project. Existing manual queue conflicts
remain waiting rather than replacing the manual operation.

Connection revision, authorizing owner, project lifecycle and hosting entitlement
are checked before enqueueing. Queued automatic jobs also check their connection
before starting. Disabling or replacing the connection fences queued work; an
already claimed operation can finish reconciling its result under the existing
account checks. It does not remove the live website. A running Node build from an
older connection can finish, but the pipeline cannot deploy it automatically.

The activity panel shows waiting, building, publishing and terminal results with
commit/time, bounded polling and safe failure text. Failed work can use the normal
upload/build/publication controls for recovery. Dedicated automatic-pipeline retry
is not implemented. All portal database consumers must upgrade together to schema
52. Provider setup, delivery against a real App and history retention still require
completion before production enablement.

Validation: focused integration checks passed for the GitHub flow, existing manual
publication/Node workflows and schema migrations. A local static flow used real
archive validation, publication claim and completion APIs; Node orchestration used
retained-release fixtures and simulated worker results. Owner/CSRF/runtime admission
and queued-disconnect fencing passed. Eleven GitHub DOM checks and portal/CLI static
analysis passed. No real provider delivery or worker-cluster rollout was performed.


## Branch-return replay handling (local schema 53, not deployed)

Duplicate detection uses the SHA-256 receipt of the authenticated raw payload,
not a permanent before/after commit pair. Separate signed events may have the
same commits, as happens when a branch moves A to B, back to A, then to B again.
Each new event receives its own processing/import/pipeline identity. Repeated
identical payloads remain acknowledged without creating new work. The processor
still compares each event with the provider's current head before importing.

The migration rebuilds the event parent table on one pinned connection and in a
transaction, preserves child processing/pipeline rows, checks all foreign keys
before commit, and restores foreign-key enforcement. All portal database consumers
must be upgraded together to schema 53. Focused checks passed for branch return,
exact redelivery, multiple projects and migration preserving publication references.
Production remains unchanged. History retention is still unfinished; receipt and
event caps remain 10,000 and return explicit retryable capacity errors.
