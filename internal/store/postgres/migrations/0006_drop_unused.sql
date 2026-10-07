-- 0006_drop_unused: remove tables that were reserved and never written.
--
-- Verified against the game protocol
-- before dropping: the client syncs its own per-player ranking progress as
-- user_*_ranking_* rows inside user_state, and its reward ledger as
-- user_reward_grant_log. It never addresses a server-side `rankings`
-- projection, a `msgid_log`, or a `push_gem_grants` ledger. Gem credits are
-- applied inside the game-RPC transaction (MutateWithGems), not from push logs,
-- so the push idempotency table has no writer.

DROP TABLE IF EXISTS rankings;
DROP TABLE IF EXISTS msgid_log;
DROP TABLE IF EXISTS push_gem_grants;
