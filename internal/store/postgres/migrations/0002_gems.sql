-- 0002_gems.sql: per-player gacha gem (quartz) balance. This is the Gree
-- PAYMENT balance the platform shim reports and the gacha consumes — free =
-- 無償石, paid = 有償石 (charge). Keyed by the game user_id. No seed row: an
-- absent player reads as 0/0.
CREATE TABLE IF NOT EXISTS gems (
    user_id BIGINT PRIMARY KEY,
    free    BIGINT NOT NULL DEFAULT 0,
    paid    BIGINT NOT NULL DEFAULT 0
);
