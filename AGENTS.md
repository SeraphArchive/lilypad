# Working on lilypad

This is a standalone Go server repository. Keep build inputs and documentation
file links inside this repository; supplied runtime data stays in ignored local
directories. Preserve existing local work. Verify behavior against current source.

For architecture, persistence or economy changes, read [docs/design.md](docs/design.md)
and [docs/stability.md](docs/stability.md). For development and test inputs, read
[docs/development.md](docs/development.md). For workflows or packaging, read
[docs/releasing.md](docs/releasing.md).

## Validation

- Run `python3 check_source.py` and `git diff --check` after source or documentation changes.
- Run `go vet ./...`, `go build ./...` and `go test ./...` for Go changes.
- Persistence, session and economy changes require `LILYPAD_TEST_DSN` pointing to a
  disposable PostgreSQL database and `go test -race ./...`. Tests create isolated schemas.
- Run `python3 build.py --version X.Y.Z` for packaging changes. `--skip-tests` is only for
  local iteration; published releases require database tests.
- Report skipped tests separately from passing tests. Local builds do not establish
  live-game parity or a successful hosted GitHub Actions run.

## Persistence and compatibility

- Preserve fail-closed save writes, opaque table-version tokens, retired process
  identities, write-time barriers and recoverable history. Unknown incremental
  schemas require explicit support; preserve omitted rows and fields on partial puts.
- Keep debits, rewards, session checks and retry receipts in the same player transaction.
  Exercise failure/retry/concurrency cases when changing these paths.
- Add forward migrations for schema changes. Keep existing migration files intact;
  verify upgrades preserve saves, balances, identity and recovery history.
- Use synthetic fixtures for CI. Captures, save exports, master data and game binaries
  are local opt-in inputs, never release contents.

## Publishing

- Keep README and config comments minimal. Maintainer details belong in `docs/`;
  the full user manual is a separate future deliverable.
- Keep the public repository identity `SeraphArchive/lilypad`.
- CI builds and tests only. The independent `release.yml`, triggered by
  `release: published`, builds the release tag and attaches assets to that Release.
- Build with `-trimpath` and `-buildvcs=false`; include dependency licenses and scan
  packaged binaries for build-machine paths. Preserve existing user configuration.
- Keep credentials, real player identifiers/data, workstation paths, external
  filesystem references and historical review/commit annotations out of public files.
  Container filesystem paths and repository-contained parent links are intentional.
- Clean current source files; leave Git history unchanged.
