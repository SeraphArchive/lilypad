# Development

Use Go at the version declared in `go.mod`. Python 3.11+ runs the shared source
checks and packaging on Windows and Linux; direct Go commands also work independently.
Source checks use Git. On Windows, use `python` in place of `python3` if needed.
Builds require only this checkout and Go module dependencies.

## Build and test

```sh
go mod download
go mod verify
go vet ./...
go build ./...
go test ./...
```

Run `python3 check_source.py` after source/documentation edits and `git diff --check`
before handing changes over. `go.sum` stays tracked. `python3 build.py` performs the
checks and packages the server for Linux amd64/arm64 and Windows amd64.
See [releasing](releasing.md) for packaging options and publication.

Database integration tests require `LILYPAD_TEST_DSN` pointing to a disposable
PostgreSQL database. The role needs schema-creation permission. Each package
uses its own random schema and removes it after testing. Use:

```sh
go test -race ./...
```

The race detector needs CGO and a supported C compiler. Hosted CI provides
PostgreSQL 16 and synthetic master fixtures. Without the test DSN, integration
tests skip; a passing unit-only run is not database validation. Some optional
tests need supplied master data even when the database is available.

## Local runtime

Copy `config.example.yaml` to ignored `config.local.yaml`, provide `db.dsn`, and
run `go run ./cmd/lilypad -config config.local.yaml`. Empty `data_dir` disables
master-driven settlement; a configured directory must contain valid decoded
master files. `/healthz` checks liveness and `/readyz` checks configured dependencies.
The example's signing mode is `noop`; game signature compatibility requires
matching client configuration. Secrets belong in local config/environment.

The config decoder rejects unknown fields. The example includes active settings;
`enforce_push_baseline` remains accepted only for old configuration compatibility
and cannot disable save protection. Keep example comments short. Environment
overrides are defined in `internal/config/config.go`; access logging is enabled
separately by `LILYPAD_ACCESS_LOG`.
Exactly one YAML document is accepted. An explicitly empty `LILYPAD_DATA_DIR`
overrides a configured master directory and disables master-driven settlement.

## Changing behavior

1. Identify the handler, storage contract and owning player transaction.
2. Use synthetic request/master/save fixtures to reproduce the behavior. Add
   meaningful regression checks for stateful failures, retries and races.
3. Preserve response shapes and exact numeric values. Keep table identities
   explicit and reject unsupported incremental mutations.
4. Verify rollback, account separation, omitted-row preservation and stale
   session rejection when changing saves/economy. Read [save integrity](stability.md).
5. Add forward SQL migrations for schema changes, and exercise upgrades as well
   as fresh databases. Keep existing migration files unchanged.

Migrations are embedded and applied in one transaction under a migration lock.
New schema updates fence queued pre-update writes. Restore tests should include
recovery history and balances rather than merely verifying schema creation.

## Optional compatibility inputs

| Variable | Purpose |
| --- | --- |
| `LILYPAD_DATA_DIR` | Additional checks against supplied decoded master data |
| `LILYPAD_FIXTURES` | Local capture inspection and structural replay |
| `LILYPAD_TEST_EXPORT` | Local archive interoperability check |

Keep those files in ignored directories such as `_external/`; they are not CI
or release inputs. `cmd/fixtool` inspects request/response pairs and replays through
the live handler stack. Replay requires a disposable database, compatible process
identity and current local baseline tokens. An account with different initial
state/wallet can produce expected structural differences. Foreign capture tokens
never authorize local writes.

`cmd/seedgen` can derive an anonymized template for local investigation. Its
numeric scrubbing does not prove a save is safe to publish: review personal
fields, account identifiers, campaign inbox and progression manually. Public
initial state keeps `user_server_gift` empty. Golden examples must be synthetic.

## Documentation

Keep current architecture in [design](design.md), persistence rules in
[save integrity](stability.md), and maintainer release steps in [releasing](releasing.md).
Record concrete behavior and limitations rather than session narratives, task
checklists, copied source code or historical review reports. Complete user-facing
configuration and deployment explanations belong in a future separate manual.
