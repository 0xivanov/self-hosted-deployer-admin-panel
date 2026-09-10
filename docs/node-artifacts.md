# Node release artifacts

`internal/nodeartifact` validates ZIP archives exported from completed Node builds
and stages them in fresh private release directories. It never executes their
contents, switches an active release or marks a build successful. Expected archive
SHA-256 must come from the trusted executor/build record, not a customer claim.
Digest equality proves byte identity, not build provenance or runtime readiness.

## Format and limits

Artifacts contain the application root, built output and pruned production
`node_modules`. Root `package.json` must be a regular JSON file with a nonempty
`scripts.start`. Runtime commands remain untrusted and require isolation. Limits:

- 100 MiB compressed, 256 MiB expanded, 32 MiB per file.
- 20,000 archive entries and at most 20,000 paths including implicit directories.
- Path length 4,096 bytes and depth 64; root package metadata at most 1 MiB.
- Regular files, directories and supported internal symbolic links only.

Validation reads every file with byte limits and ZIP checksum verification before
creating a release. It rejects duplicate paths, absolute/traversal paths, backslash
or colon paths, invalid UTF-8/control characters, special files and file/link parent
conflicts, independent of entry order. `.git`, `.npmrc`, `.env` and `.env.*` entries
are rejected at any depth. Do not put build credentials into exported files.

Internal command links such as `node_modules/.bin/tool -> ../tool/bin.js` work.
Links must resolve to an existing regular file within the same archive, with at
most 32 link steps. Absolute, escaping, dangling, cyclic and directory links are
rejected, as are paths traversing an intermediate link. Parent steps are permitted
only at the start of a link target, avoiding ambiguous `link/../file` resolution.
Linked workspaces/directory symlinks are outside this initial format.

## Staging

`Extract` requires an operator-controlled private root, unavailable to customer
processes while staging. It creates an unpredictable `release-<64 hex>` directory
and confines writes through `os.Root`. Directories are 0700; regular files are 0600
or 0700 when the archive has an executable bit. Archive ownership, setuid/setgid and
group/other permissions are never imported. It creates links only after all files,
so links cannot redirect extraction writes. Files and directories are synced before
return. Errors or cancellation remove the newly created partial directory.

A successful result is a directory reference plus validated archive metadata.
Existing releases are left in place. Callers still need immutable storage, retention
quotas, crash/orphan reconciliation, database binding to the execution/source/
toolchain, transfer verification and health-checked activation/rollback. Directory
names alone are not completion evidence; process/power-loss recovery is unqualified.
The production exporter must prove customer build processes have stopped before
reading their output tree. Starting a staged release on the portal or Mac is never
part of this API.

## Verification

Tests cover npm command links/chains, actual reopening through a staged link,
private/stripped permissions, retaining a prior release, path and link attacks,
ZIP CRC/digest mismatch, oversized declared files and cleanup after cancellation
once a staging file exists. Full race-enabled integration tests and Go vet pass.

The disposable Linux rehearsal additionally packages the trusted fixture after
its build/prune steps, validates and stages that ZIP with `node-artifact-check`,
and starts the extracted release for HTTP readiness and shutdown checks. This
qualifies the connected fixture path, not hostile-output export or production
artifact publishing. Reproduce with `node-lab-setup.py --worker` as described in
`node-build-rehearsal.md`.
