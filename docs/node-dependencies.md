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
