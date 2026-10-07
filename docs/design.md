# Design

lilypad serves three HTTP surfaces from one Go process. PostgreSQL owns persistent
accounts, player tables, wallet balances, process bindings and recovery history.
Decoded master data is an optional, read-only runtime input. The server does not
bundle game binaries, asset bundles, master databases or captured player saves.

## Components

| Component | Responsibility |
| --- | --- |
| `cmd/lilypad` | Configuration, database/master wiring, HTTP dispatch and shutdown |
| `internal/httpapi` | Game routes, codec envelopes, sessions, save sync and economy RPCs |
| `internal/portal` | Web accounts, takeover credentials, archive import/export, platform shim |
| `internal/store` / `internal/store/postgres` | Storage contracts and transactional PostgreSQL implementation |
| `internal/deltacomm` / `internal/model` | Partial-row merging, explicit table keys and initial state |
| `internal/master` / `internal/gem` | Master-data access, rewards and free/paid quartz rules |
| `internal/codec` / `internal/sign` | Game body encoding and optional response signing |
| `internal/fixtures` / `internal/replay` | Opt-in local protocol diagnostics |

`/api/*` serves the game API, `/v1.0/*` serves the platform shim, and remaining
paths serve the portal. `/healthz` is liveness; `/readyz` checks configured
dependencies, including database connectivity. Startup loads configuration,
applies embedded migrations, loads master data, then admits requests. Shutdown
drains admitted requests for up to 30 seconds before closing resources.

## Transport and identity

Game bodies encode JSON as UTF-8, gzip it, then apply the chained-XOR LCG codec.
The cipher uses seed 1156, multiplier `0x015A4E35` and increment 1. Wire numbers
retain exact JSON representations. Optional response signing uses the client
protocol's RSA-1024 / PKCS#1 v1.5 / SHA-1 scheme. This compatibility scheme does
not replace HTTPS. Platform requests use plain JSON.

Portal credentials bind to a durable 32-hex platform XUID. Login resolves that
XUID into the numeric game `userId`. Subsequent game requests use the numeric
identity; resolving it as a new XUID would create an unrelated account.
Takeover credentials use Argon2 password hashes at rest. The platform password
wire encoding is reversible and is decoded before verification. See
[platform protocol](platform-protocol.md) for login payloads.

The game client must redirect both server surfaces and send a fresh
`x-lilypad-session` identity per process. A compatible
[clientpatch](https://github.com/SeraphArchive/clientpatch) is maintained separately.
The process identity fences concurrent/stale clients; it is not account authentication.
Current game-facing identity handling trusts compatible client headers and does
not provide official platform identity verification or anti-cheat enforcement.

`app/start` returns configured asset version/hash values, or values resolved from
a matching client version report. Asset delivery remains outside this server.
Reports do not justify accepting stale save writes or incompatible master schemas.

## Saves and transactions

`confirm` reconciles table versions, `pull` returns snapshots, and `push` applies
incremental changes under a per-player transaction lock. Table versions are
opaque tokens allocated by a database sequence, not hashes clients recompute.
The canonical hashing helper remains useful for request digests and legacy migration.

Collection identity comes from explicit table key metadata. Partial puts preserve
omitted rows and fields; singletons have explicit cardinality. Client full
replacements, unknown delta schemas, stale/missing baselines and ambiguous nested
array merges fail atomically. Trusted imports use a separate full-snapshot path.
Imports intentionally delete tables omitted from the archive.

Database triggers archive mutations, maintain version tombstones and fence game
processes after external edits. Schema upgrades revoke old save sessions and set
a write-time barrier. Rejected packets are retained for manual recovery without
being replayed automatically. [Save integrity](stability.md) defines the complete
session, retry, history and recovery contract.

The embedded initial-state template contains neutral starter rows and empty
tables, not a player account. Personal text, timestamps and instance identifiers
are cleared; campaign inbox gifts are empty. Protocol/master identifiers define
schema and starter content and are distinct from player identifiers. New wallets
start at zero. The configured asset version populates the initial `user_version`.
On account creation, the neutral profile's zero `_registeredAt` becomes the
account's database creation time; later logins and imported nonzero dates retain
their original registration time.

## Economy

Quartz lives in the platform wallet's `free` and `paid` balances, not in game
currency tables. `internal/gem` validates nonnegative, nonoverflowing balances and
spends free quartz before paid. A narrow `BalanceProvider` adapter resolves XUID
and exposes stored balances to the shim as decimal strings.

For requests declaring a positive quartz cost, `gem.validate_cost: true` uses a
master-defined quartz cost when available. New/unknown or non-quartz master
entries still fall back to the declared cost; a zero declared cost remains zero.
Setting the flag false always trusts the declared cost. This compatibility policy
is not complete payment validation and should not be described as anti-cheat.
Draw payment, generated rewards and exact
retry responses commit together. A failure rolls back all mutations; there is no
best-effort compensating refund contract. Missions, gifts and daily rewards use
master-defined grants. Repeated mission claims and paid operation retries must
not pay or consume twice.

Missing master data does not settle rewards. An explicitly configured unreadable
or empty master directory fails startup; an empty `data_dir` intentionally disables
master-driven settlement. Unsupported reward categories fail without consuming
payment. The implemented reward/economy boundaries are recorded in
[save integrity](stability.md). There is no real-money purchase settlement.

## Boundaries

Battles remain client-authoritative. Ranking/miscellaneous compatibility routes
include limited responses; route registration is not a claim of full game parity.
Protocol correctness needs fresh evidence for each intended client build.
Synthetic CI checks transactions and invariants, while opt-in local replay checks
response structure. Neither establishes full official-server equivalence.

Portal sessions are in memory. Multiple portal replicas need affinity or a
shared session implementation. Without `db.dsn`, the process runs a nondurable
development portal and does not provide save sync. Persistence requires PostgreSQL.
