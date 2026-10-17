# Quartz transaction history

## Client contract

The Steam client's quartz history uses `GET /v1.0/payment/history`, separate from
encoded game requests. Its GameLib success envelope is `result: "OK"` with an
`entry` containing `transactions`, decimal-string `offset` and `limit`, and a
boolean `has_next`. Empty transactions are `[]`, not `null`.

Each transaction has decimal-string `transaction_type`, `balance_change_charge`
and `balance_change_free`, a JST date formatted `yyyy-MM-ddTHH:mm:ss+0900`, and a
`reason` containing a JSON **string**, not an embedded object. Issuance includes
empty `expire_charge` and `expire_free` strings. Spending uses negative changes
and issuance uses positive changes. The client requests 20 entries per page.
Some captured issuance rows encode the zero paid change as numeric `0`; the SDK
casts it to a string and then falls back to zero when parsing fails. The shim
consistently emits decimal strings, including `"0"`, so nonzero changes are never
lost to that string cast.

`GreeGameLib.Transaction` defines consumption as type 1, real-money paid-quartz
purchase as type 2, and free-quartz issuance as type 3. The native presenter
renders type 2's reason verbatim; other types interpret the reason JSON. Never
use type 2 for free grants or administrative wallet replacements.

## Reason semantics

`Lily.GamelibReasonType` and `UIGemHistoryPresenter.CreateReasonText` define:

| Value | Name | Display input |
| --- | --- | --- |
| 0 | NONE | Empty description |
| 1 | LOTTERY | `masterLotteryId` -> MasterLottery.masterLotteryShopId -> MasterLotteryShop.name |
| 2 | RECEIVE_CLIENT_GIFT | `masterReasonId` -> MasterReason.title |
| 3 | PRISM_BATTLE_RESET | Empty description in this presenter |
| 4 | EXPEDITION_RESET | Empty description in this presenter |
| 5 | RECEIVE_SERVER_GIFT | `masterReasonId` -> MasterReason.title |
| 6 | RECEIVE_MISSION | `masterMissionId` -> MasterMission.description |
| 7 | RESET_DAILY_STAMINA | Empty description in this presenter |
| 8 | LIFE_RECOVERY | `recoveryNum` in the localized recovery message |

These are localized by the client using its own master/string tables; the server
does not send a fabricated Japanese or translated description. For gift types,
reason ID zero resolves the MasterReason label `reason.default`, whose title
refers to items exceeding the holding limit. It is not a generic wallet-adjustment
description. Mission reasons include `masterReasonId: 0`, but the presenter ignores it. A reason ID is neither
a reward ID, a gift instance ID, nor a login-bonus definition/content ID.
For example, reason `114001003` is `Reason.NormalLogin_04` (ordinary login day 4)
and `114000207` is `Reason.LiveHighScoreReward`, not their quartz reward IDs.

A gift may carry a newer reason ID absent from the server's master snapshot.
Preserve that numeric ID without substituting an older title or refusing it merely
because the local reason table lacks the entry. The client's master supplies the
text. Zero remains zero on the wire; the client alone applies its default lookup.

The relevant native presenter entry points are CreateReasonText at RVA
`0xE7AF60` and CreateUsageHistoryEntity at `0xE7B3B0`. The latter renders signed
amounts using transaction type and parses dates; GameLib history parsing requires
string offset/limit and boolean has_next. DomainClientGiftRepository.AddInternal
at RVA `0x187D6F0` resolves a nonempty reward reason label when creating the gift;
an empty label leaves its reason ID zero. Captured platform history confirms the
same wire structure and separate entries for each received mission or gift.

## Implementation design

The gem module owns a typed transaction history model and reason serialization.
The PostgreSQL adapter appends `gem_transactions` under the existing player lock
and in the same transaction/savepoint as wallet changes, reward rows and retry
receipts. A wallet write or journal failure rolls back the whole operation.
The operator-only `player_data_history` remains independent recovery history.

`store.GemChange` retains its aggregate add/consume amounts and gains optional
per-credit provenance plus debit provenance. When credit provenance is supplied,
its positive amounts must sum exactly to the aggregate credit. A batch of mission
or gift claims therefore produces individual income rows without additional
wallet writes. Actual free-first before/after differences determine the debit's
free and paid columns. Zero changes, repeated claims and cached request retries
produce no new transactions.

Game handlers supply reasons from successfully settled state: lottery ID for a
quartz draw, mission ID for mission claims, and the persisted gift's reason ID for
gift claims. Login-bonus generation resolves MasterReward.masterReasonLabel via
MasterReason.label and stores that ID in the gift; receipt preserves it rather
than substituting the reward or bonus ID. Daily update only creates unreceived
gifts; income is recorded at receipt. Life recovery carries its recovery count;
spirit recovery does not reuse a life-specific description. Existing settlement paths
without a proven GameLib reason mapping use NONE rather than inventing a gift,
mission or purchase. In particular, a MasterItemLottery ID cannot be substituted
for the MasterLottery ID used by reason type 1. Standalone free adds/consumes
likewise have no provenance.

The new forward migration creates an indexed, managed journal without altering
old migrations or deriving fictional reasons from recovery snapshots. Existing
wallet balances, saves and recovery entries survive. History starts when journal
recording is introduced. Import, explicit Set and direct SQL balance replacement
remain recovery operations, not purchase/issuance transactions; they do not
backfill, erase, or manufacture game history. Transfer archives continue carrying
a wallet snapshot, not official or local transaction history.

The shim resolves the requestor through its existing XUID mapping and reads only
that player's journal. Offset defaults to 0, limit to 20; offset must fit a
nonnegative signed 32-bit integer and limit is bounded to 1..100. Invalid or
ambiguous parameters and identity/storage errors return a non-success envelope.
Read one extra entry to determine has_next, ordering by journal ID descending for
stable ordering even when a batch shares a timestamp. Configured storage failure
must never be reported as an empty successful history.

## Verification

Use synthetic master/save definitions and a disposable PostgreSQL database.
Cover per-gift/per-mission reasons, default reason zero, mission batch deduplication,
loop-claim intervals, daily issuance timing, quartz versus ticket draws, mixed
free/paid spending, failed settlement rollback, cached retry, concurrency,
account separation, pagination including empty/final pages, query validation,
wire field types, and upgrades preserving balances and recovery history.
Live-game rendering is a separate compatibility check, not established by tests.
