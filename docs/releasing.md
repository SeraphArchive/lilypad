# Releasing

The public repository is [SeraphArchive/lilypad](https://github.com/SeraphArchive/lilypad).
Publish the reviewed source tree as a fresh repository; Git history cleanup is
not part of the build. Local private configuration and inputs stay outside that tree.

## Validation and packaging

From a checkout with Go and Python 3.11+:

```sh
python3 build.py --version X.Y.Z --require-database
```

Set `LILYPAD_TEST_DSN` to a disposable PostgreSQL database for release validation.
`--require-database` makes missing database validation an error. `--skip-tests` is for
local packaging iteration and cannot be combined with `--require-database`.
Ordinary unit tests intentionally skip opt-in capture/master/export checks.

The script checks source hygiene, module integrity, formatting, vet and tests,
then cross-compiles static server binaries with `-trimpath` and `-buildvcs=false`.
`--platforms linux-amd64` selects a subset during local iteration. Defaults are
Linux amd64/arm64 and Windows amd64. `--race` enables race tests as used in CI; local race testing
also requires a C compiler. Every ZIP contains:

- `lilypad` or `lilypad.exe`;
- `config.example.yaml`, `README.md`, `LICENSE` and `lilypad.version`;
- `licenses/` with the dependency inventory and upstream license/notice files.

ZIPs and SHA-256 checksums are written under `dist/releases/<version>/`.
Only explicitly staged files enter the package. Private configs, keys, master data,
captures, databases and game files are excluded. Missing dependency licenses and
build-machine paths in staged files fail packaging. MIT covers lilypad code;
Go and module dependencies retain their respective licenses.

## Workflows

`ci.yml` runs on pushes, pull requests and manual dispatch with read-only
repository permissions. It checks source hygiene, formatting, module integrity,
vet, race-enabled tests against PostgreSQL 16, cross-platform packages and the
Linux amd64/arm64 Docker build. Validated ZIPs and their SHA-256 files are uploaded
to Actions Artifacts as `lilypad-linux-and-windows`; the native Windows job uploads
`lilypad-windows-native`. Artifacts are retained for 14 days. CI does not create
Releases or publish container images.

Publish a GitHub Release with a `lilypad-vX.Y.Z` tag (prerelease suffixes are
allowed). The independent `release.yml` checks out that tag, runs database-backed
tests and packages, then builds and pushes a Linux amd64/arm64 image to GitHub
Container Registry before uploading ZIPs/checksums to the existing Release. It does
not create a Release, rewrite notes or change its prerelease flag. Re-runs replace
matching asset names and image tags. A failed validation publishes no new outputs;
registry publication and Release attachment are separate operations, so an upload
failure after the image push can leave the image available before the assets.
Actions must be allowed and the release job needs `contents: write` and
`packages: write`; registry login uses the built-in `GITHUB_TOKEN`.

The image name is `ghcr.io/<repository-owner>/<repository-name>` in lowercase
(`ghcr.io/serapharchive/lilypad` for the public repository). Images carry both
`X.Y.Z` and `lilypad-vX.Y.Z` tags, plus `latest` for stable, non-prerelease Releases.
Prereleases only update their explicit version tags. OCI labels identify the
version, source repository and checked-out commit. Image visibility is managed
in GitHub Packages; allow public visibility there if anonymous pulls are intended.

## Before public release

- Require green CI and inspect a downloaded package on each advertised platform.
  Check startup, PostgreSQL migrations, portal registration, readiness and shutdown.
- With a disposable save, verify the intended client build's login, pull/push,
  retries, import/export and stale-session rejection after an upgrade/external edit.
  Synthetic database tests do not establish live-client parity.
- Test an upgrade from the supported previous schema with a database backup and
  a verified restore procedure. History and retry receipts grow without automatic
  expiry; budget storage and retain recoverable copies.
- Review dependency notices after module updates. Confirm no private account data,
  captures or master files are present in the source tree or package.
- Configure repository branch protection/required CI and vulnerability reporting.
  Consider reviewed Action pins and executable signing before distribution.

## Container maintenance

`deploy/Dockerfile` builds the same trimmed server as a nonroot distroless image.
BuildKit cross-compiles on the builder's native platform, so publishing both Linux
architectures needs no emulation. The image includes `lilypad.version` and dependency
licenses. The CI and release builds reuse scoped GitHub Actions build caches.
`.dockerignore` keeps local data, secrets and output out of the build context.
`deploy/docker-compose.yml` provides PostgreSQL 16 and a named data volume.
Run Compose from `deploy/` with local `config.yaml` and `POSTGRES_PASSWORD` set.
Compose leaves economy disabled when `LILYPAD_DATA_DIR` is empty. Configure a
real master directory through that variable to enable economy operations.

Container paths are intentional runtime paths, not workstation dependencies.
Back up PostgreSQL before upgrades; deleting the named volume loses persistent
state. Full user deployment/configuration documentation is a separate deliverable.
