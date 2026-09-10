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
