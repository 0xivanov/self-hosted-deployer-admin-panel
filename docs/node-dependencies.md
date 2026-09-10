# Node dependency download stage

`internal/npmfetch` prepares registry download lists and verifies downloaded bytes
without executing npm, extracting packages or running their scripts. This stage is
not yet wired to the build worker or an offline npm cache.

## Implemented contract

`FromSource` revalidates the source ZIP and assigned SHA-256, reads the exact root
package-lock.json, and requires lockfile version 2 or 3 with a packages map and root
entry. It collects all locked platforms and dependency types; npm ci must still
validate package/lockfile consistency and choose platform-specific packages inside
the isolated builder. The initial limit is 1,000 package entries besides the root.
Repeated tarball URLs are deduplicated, while conflicting integrity values fail.
Bundled dependencies need no separate download; local links/workspaces and git/file
sources are not supported in this initial fetch path.

The source must not contain a root npm-shrinkwrap.json. npm gives it precedence over
package-lock.json, so accepting it would inspect a different lockfile from the one
npm actually uses. See the [official lockfile documentation](https://docs.npmjs.com/cli/v11/configuring-npm/package-lock-json/).

Each tarball requires an HTTPS URL on exactly registry.npmjs.org, no credentials,
custom port, query, fragment or path traversal, and a canonical SHA-512 integrity
value. Downloads disable redirects, ambient proxies and content decompression;
request/header/connect timeouts and a 25 MiB per-tarball limit bound each request.
Only bytes matching the expected SHA-512 are returned. Transport errors are generic.
No credentials or package scripts are involved. Integrity proves correspondence to
the submitted lockfile, not that the package is benign.

## Qualification and remaining integration

Unit tests use fake transports and synthetic source ZIPs: accepted downloads,
source mismatch, unsupported lockfiles/sources, shrinkwrap precedence, hash
conflicts, tampered bytes, redirects, proxy disabling, header/chunked size limits,
encoded responses and provider errors. No registry packages were installed or
executed by these tests.

The fetch service still needs VM/firewall isolation from private networks, bounded
aggregate bytes/concurrency, durable source/operation binding, cache/artifact
storage and quota/recovery behavior. Exact hostname and TLS validation alone are
not a network isolation boundary. The builder must remain offline, including
install hooks and build scripts. Verified tarballs need a qualified import path
into its job-local npm cache, followed by an actual third-party dependency build
inside the restricted VM. Package expansion/native compilation and artifact export
remain subject to resource and filesystem limits. No public build route is enabled.

## Offline npm cache rehearsal

On 2026-09-10 the restricted ARM64 Node VM successfully installed and executed
is-number 7.0.0 from a newly seeded offline cache. The checked-in dependency fixture
pins its registry tarball and SHA-512 from the registry's version metadata:
https://registry.npmjs.org/is-number/7.0.0

The lab prefetch helper uses the production FromSource/Fetch functions, limits the
fixture to 10 tarballs/50 MiB aggregate storage, and writes verified tarballs plus a
source-bound manifest. It runs trusted Go download code on the Mac without running
npm or package code. The tarballs are copied explicitly as read-only VM inputs.
This is a rehearsal transport, not a qualified production fetch service.

Inside the restricted service, a new job-local cache first fails npm ci in offline
mode, proving the dependency was not inherited from another cache. The harness
rechecks source identity and each tarball's SHA-512, then seeds the cache with
`npm cache add <local-tarball> --offline --ignore-scripts`. With npm offline mode
and the private network namespace still enabled, the actual build plan installs,
builds and starts the app. HTTP output confirms the imported package was executed.
Startup-hook suppression, shutdown and broken-build checks also pass.

Reproduce with `python3 deploy/qualification/node-lab-setup.py --dependency` after
starting the named disposable VM. Without the flag, the original dependency-free
rehearsal still passes. Both variants clean local temporary inputs and guest build
folders; stop the VM after running. The dependency ZIP digest for this run was
`a24bf064e7c4a6f2d50edc8875ac468a5c41dcd5f6a64cdea2144082c09e5739`.

This qualifies one small pure-JavaScript dependency and cache import behavior under
Node v24.20.0/npm 11.19.0. Transitive/native packages, cache corruption/eviction,
production artifact storage/quotas and source-bound worker dispatch remain work.
Registry signatures/provenance were not verified; SHA-512 only binds downloaded
bytes to the pinned lockfile. All execution stayed inside the restricted lab VM.

## Durable private bundle storage

`DownloadBundle` now provides the reusable storage stage. It revalidates the source
and dependency plan, creates a fresh private directory under an operator-owned job
root, and downloads packages sequentially under a five-minute context. Each returned
payload is independently SHA-512 checked even when a custom downloader is supplied.
Tarballs use content-derived filenames and owner-only permissions. The total stored
payload plus manifest is capped at 100 MiB; per-tarball limits still apply.

Files are synced before a pending manifest is atomically renamed to bundle.json;
bundle and parent directories are synced before returning its directory name and
manifest SHA-256. The manifest retains source identity, URLs, content filenames,
integrity and byte counts. Consumers must bind the returned manifest digest to the
trusted build record and reverify transferred content before use. The bundle is
not an npm cache itself, and no package is unpacked or executed by this function.

Ordinary failure/cancellation removes the newly created partial directory.
Successful retention, aggregate quotas across jobs, ownership mapping and process-
crash orphan reconciliation remain the worker/storage layer's responsibility.
A partially created directory must never be considered complete merely because it
exists. A process/power-loss rehearsal is still required; sync ordering is currently
covered by implementation review and ordinary reopen/failure tests.

The lab prefetch helper now uses this shared component (with its additional
10-package fixture limit), replacing its original 50 MiB temporary writer. Its
restricted offline dependency build passed again after the change. Tests verify
private permissions, reopened manifest/content identity, missing temporary manifest
and cleanup after provider errors, bad bytes, quota overruns and cancellation.

## Reopening a completed bundle

`VerifyBundle` accepts the bundle directory and manifest SHA-256 retained by the
trusted worker, plus the expected source SHA-256. It rejects unsafe directory/file
names, non-private paths, symlinks, incomplete manifests, unexpected files and
mismatched identities. Every stored tarball is reread with byte limits and checked
against its filename SHA-256, integrity SHA-512 and recorded size. Aggregate byte
and entry limits are checked again. A pending manifest is not a completed bundle.

The lab prefetch helper now verifies its completed bundle before handing it off.
Consumers still need to store the expected manifest digest in a trusted build
record and protect the bundle from mutation between verification and import.
This method does not delete abandoned directories or qualify process/power-loss
recovery. Tests cover reopen success, changed source/manifest identity, altered
bytes, missing manifests, extra pending files, symlinks and public permissions.

## Binding bundles to build jobs

Schema 19 stores an immutable dependency-bundle reference on a Node build.
`BindNodeBuildDependencies` is trusted worker access: it requires the exact running
job/execution and unexpired lease, with the submitter still verified, enabled and
an owner/developer. It copies the assigned source, verifies the bundle, and checks
its exact URL/integrity set against that source's lockfile. An internally consistent
bundle for a different dependency set is rejected even if it claims the same source.

After filesystem validation the store repeats lease and permission checks in the
binding transaction. Rebinding the same directory/manifest digest is idempotent;
replacing it with a different reference is rejected. `NodeBuildDependencies` returns
the persisted reference only to the current authorized worker lease. The reference
contains a relative directory and manifest digest, never an arbitrary absolute path
from a customer. Worker configuration still determines the assigned storage root.

Bindings survive restart. Consumers must reverify files before cache import and
keep storage immutable through use. Stale/expired execution cleanup needs a separate
reconciliation path; this API cannot grant a stale worker access again. No public
build route or VM dispatcher is introduced by this change.

## Worker preparation

`PrepareNodeBuild` connects queued-build claiming, dependency downloading and
verified bundle binding. It renews the worker lease every 20 seconds while those
operations run. Renewal failure cancels downloads; binding and a final handoff
renewal also recheck the submitter's authorization. A successful result includes
the execution identity, refreshed lease and persisted bundle reference. The next
executor stage must continue renewal before dispatching.

A non-nil result on error retains the claimed execution identity and any completed
bundle for reconciliation. The job stays running and cannot be automatically
claimed again. Failed downloads remove their partial directory; completed bundles
are retained because a binding may already have committed. Preparation does not
create a VM, import an npm cache, execute customer code or activate a release.

Integration tests exercise the complete claim/download/bind path, actual lease
renewal during downloading, authorization revocation, provider failure, partial
file cleanup and refusal to redispatch a running job.
