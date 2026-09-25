# GitHub deployment

Status: implementation started September 25. Repository archive preparation and
push signature validation are source components, not a connected customer
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
