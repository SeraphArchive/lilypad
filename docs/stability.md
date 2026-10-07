# Save integrity and compatibility

Save mutations serialize per player. The complete game request executes inside that transaction,
including session-generation validation and any persisted retry result. Nested mutations use
savepoints, so a failed reward write cannot commit a debit through an outer successful HTTP response.

Portal exports read tables, hashes, and quartz under the same player lock. Imports replace tables,
optionally replace quartz, revoke the session, and clear old retry results in one transaction.
Invalid archives, missing collection keys, duplicate row identities, and invalid balances are rejected
before replacement. Keep a backup before intentionally importing an archive: tables absent from it
are still deleted, as required by the full-snapshot format.

Save numbers remain exact, including 64-bit identifiers and exponent notation. The wire `hashes`
are opaque 32-hex table-version tokens; clients must echo the tokens they read, not compute them.
Migration `0008_save_safety` archives existing table data before changing tokens and preserves all
rows and wallet balances. Versions come from a database sequence, so even A -> B -> A cannot revive
an old baseline. Deleted tables retain a token and can be pulled as empty rows.

Every client push validates each touched table's baseline, even when the old configuration key
`enforce_push_baseline` is false. Missing/empty baselines cannot overwrite an existing version.
Client `replaceItems` is refused; intentional full replacements use the archive import path.
Collection keys and singleton kinds are explicit. Unknown incremental table mutations, malformed
singleton rows, and ambiguous put/delete operations are rejected atomically. Opaque newer tables
can still be imported/exported unchanged. Partial puts retain omitted fields, including nested
object fields, so an older client does not erase fields introduced by an upgrade/external editor.
Partial nested object-array replacements that would drop stored fields are refused when the server
has no declared nested key; it does not guess object identities. Such unsupported writes need an
updated schema/client or a reviewed external edit, rather than a silent destructive fallback.
Every replacement object must carry the complete stored field shape; matching a field on a
different object or at an assumed array position cannot protect omitted fields.

`user_world_state._localFlag` is an explicitly supported snapshot array keyed by
its string `_id`; `_value` is a signed 32-bit integer. Supplied snapshots replace
the known flag set, including an empty array when a world resets. Retained flag
identities preserve omitted unknown fields. Removing a flag with unknown stored
fields is refused; unrelated arrays keep the generic protection above. Omitting
`_localFlag` from a partial world-state put preserves it.

## Client sessions

Use a compatible [clientpatch](https://github.com/SeraphArchive/clientpatch) that sends `x-lilypad-session`. Its value is generated once
per process and is never persisted between launches. The server compares this identity with the active
login and also checks the session generation inside the transaction. A Portal import revokes the
session regardless of how far the request counter has advanced; the game must log in and pull again.

Clients without this header cannot establish a save session or write. A request counter, however
large, cannot substitute for a process identity. Migration, import, explicit wallet replacement,
and external SQL edits retire all previously known process identities. Restart the game with the
updated patch and pull the current save; the old process cannot renew its identity by advancing
its counter. Process identity is concurrency protection, not public account authentication.

DCC can keep a persistent packet while rebuilding its hashes after a pull. The server therefore
also retains a write-time barrier at a save switch. `savedTime` must be positive, no later than
the server clock, and strictly later than that barrier. Packets created before the switch (or in
its same whole second, where ordering is ambiguous) are refused even if their hashes were rebased.
Keep client/server clocks synchronized. Conflict responses pause the old queue instead of falsely
acknowledging a save. Refused packets that reach push validation are retained in recovery history.

## Retry contract

Lottery draws, item-lottery draws, select-ticket exchanges, and resource recoveries persist their exact
HTTP result in the same transaction as the save/wallet changes. A retry must reuse `Idempotency-Key`,
or otherwise the same `x-msgid`, and the same decoded request payload. It receives the original result
without another debit or roll. Reusing the identifier with different content fails. Results are
scoped to the process identity and survive an ordinary re-login and server restart. They are not
automatically evicted after a fixed number of operations, which could turn a delayed retry into a
second debit. Import/wallet replacement retires the old processes and clears their retry results.
Requests without an operation identifier are refused on retry-sensitive routes.

Push retries use the canonical packet body (excluding transport `hdr`) when no explicit
`Idempotency-Key` is supplied: DCC regenerates `x-msgid` on transport retries. A repeated queued
packet gets its original response without rewriting newer progress. Different/stale packets still
have to pass the version and save-switch barrier checks.

## Recovery history and external changes

Database row triggers cover actual inserts, updates and deletes to `user_state` and `gems`, including
SQL outside the game/server. They retain before/after data in `player_data_history`, update table
versions/tombstones and retire old game processes for external edits. Conflict-ignore inserts have
no history/version side effects. Transactions that roll back leave no misleading committed history.
`TRUNCATE` is refused because it bypasses row recovery; use `DELETE` for intentional maintenance.
Game account identities cannot be renamed, or deleted while saved/recoverable data exists; doing
so would make intact progress appear to belong to a new account. Transfer archives explicitly.

History and retry receipts have no automatic destructive expiry. Budget database space and back up
the database; deliberate retention changes must preserve needed recovery copies and retry safety.
Migration `0009_registration_time` fills only zero registration timestamps in singleton profiles
from the account creation record. It preserves other fields and nonzero imported dates, records
before/after recovery history through the existing triggers, and retires pre-upgrade sessions.
To inspect one player's history, use a parameterized query:

```sql
SELECT id, transaction_id, table_name, operation, changed_at, old_data, new_data
FROM player_data_history WHERE user_id = $1 ORDER BY id DESC;
```

`@gems` entries contain wallet before/after values. `@rejected_push` entries contain refused deltas,
their baselines, creation time and reason. Review these against the current save; do not automatically
apply an obsolete packet.
Entries sharing `transaction_id` belong to the same atomic operation. Version metadata and recovery
copies reject direct SQL edits, so an editor cannot accidentally hide a save or erase its history.

Restoring a table before-image can use this parameterized upsert:

```sql
INSERT INTO user_state(user_id, table_name, rows, hash)
SELECT user_id, table_name, old_data, '' FROM player_data_history
WHERE id = $1 AND old_data IS NOT NULL AND table_name NOT LIKE '@%'
ON CONFLICT(user_id, table_name) DO UPDATE SET rows=EXCLUDED.rows, hash=EXCLUDED.hash;
```

The triggers generate the restored version and retire clients; do not disable triggers or set the
server's internal-write marker in external tools. Full database/table drops are outside this row
recovery mechanism. Restore operations intentionally replace state and require a reviewed choice
of the player's history entry.

## Economy rules

Ordinary mission batches deduplicate IDs. Loop missions grant only the interval between stored and
new received counts, so repeating the same received count pays nothing. Repeated loop-mission IDs
within one request merge into one row, and delayed claims retain newer saved completion counts.
Regular weekly mission gauge points are represented by the mission's received state; the client
derives their total from master rewards. Settlement retains mission progress, expiry and other
stored fields. Weekly gauge-point rewards on loop missions remain unsupported.

Reward accumulation and additions to inventory, currencies, limit-break power and recovery
resources check integer overflow before committing. Invalid amounts roll back the entire claim
and payment. A lottery that consumes and rewards the same item debits it before applying the grant;
valid final balances remain accepted, and total acquired quantity counts only the reward.

Missing master data never marks gifts/missions received or advances daily reward settlement.
Daily stamps are created explicitly, and weekday missions use the JST 04:00 reset date.
Missing reward groups, unsupported server grants, missing duplicate-card conversions and absent
reward destinations reject the whole settlement. Positive term-stock costs require distinct,
existing item instance IDs with a sufficient combined balance; a missing instance list cannot waive
payment. The total cost is spent once in selection order, preserving any surplus and unselected rows.

Item lotteries enforce request/daily/total/drop limits, update pity counters, force a remaining pickup
at its guarantee threshold, and apply completion rewards with the master's terminate/reset policy.
If a batch exhausts/completes its box early, only the actual draws are charged and counted.
Supported reward categories are items, currency, quartz, term-stock items, and limit-break power.
Other categories, including accessories requiring generation rules, fail the transaction without
consuming payment rather than silently discarding the reward. Live-client fidelity for box completion
still needs a captured item-lottery session; regression tests use synthetic master definitions and
the extracted client schema/enums.

## Portal and diagnostics

Email verification gates both Portal authenticated access and platform takeover when configured.
Registration remains successful if mail delivery fails and preserves the one-time takeover password
in the response. The login form can resend verification using the account password. Links expire after
24 hours, with a one-minute resend cooldown. SMTP is bounded by cancellation/deadlines.
Verification tokens are consumed atomically once. Token replacement and its resend cooldown
are enforced in storage, including concurrent requests.

Portal web sessions/version-report caches have bounded eviction. Template errors produce coherent HTTP failures,
save-read errors remain visible, and sensitive responses use `Cache-Control: no-store`. Access logging
does not read bodies and redacts credentials from headers/queries. The server still has no forgotten
login-password reset flow; that remains a separate account-recovery feature.
Portal rejects cross-origin state-changing browser requests, including the initial login.
The platform shim returns failure when configured account/wallet storage is unavailable, a balance
is invalid, or takeover binding cannot be persisted; it does not acknowledge those failures as success.

## Tests

`LILYPAD_TEST_DSN` enables real database tests. Each test package creates and later removes its own
random schema, so concurrent packages cannot truncate one another's saves. The test database role
needs permission to create schemas. Synthetic master fixtures exercise economy transactions in CI
without shipping game data. `LILYPAD_DATA_DIR` and `LILYPAD_FIXTURES` enable additional local checks;
`LILYPAD_TEST_EXPORT` enables the optional real-export interoperability test.

`fixtool replay` validates every row's structural shape, stable status codes, and array cardinality;
hashes/generated IDs/timestamps/RNG values remain opaque. It exits nonzero on mismatches. Mature-account
captures replayed against a fresh seed can produce expected diagnostic mismatches and should not be
used as a zero-difference regression oracle without restoring their initial state and wallet.
Captured game requests also need a current process identity and locally read version tokens;
foreign capture hashes are deliberately not accepted as permission to overwrite a local save.
