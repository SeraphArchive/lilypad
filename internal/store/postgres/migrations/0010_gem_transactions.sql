-- Player-visible game settlement is distinct from operator recovery snapshots.
-- Existing balances/history cannot be assigned fictional transaction reasons.
CREATE TABLE gem_transactions (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES accounts(user_id),
    changed_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    transaction_type INTEGER NOT NULL,
    free_delta BIGINT NOT NULL,
    paid_delta BIGINT NOT NULL,
    reason JSONB NOT NULL CHECK (jsonb_typeof(reason) = 'object'),
    CHECK (
        (transaction_type = 1 AND free_delta <= 0 AND paid_delta <= 0
            AND (free_delta < 0 OR paid_delta < 0))
        OR (transaction_type = 3 AND free_delta > 0 AND paid_delta = 0)
    )
);
CREATE INDEX gem_transactions_player_page ON gem_transactions(user_id, id DESC);
CREATE TRIGGER gem_transactions_managed BEFORE INSERT OR UPDATE OR DELETE ON gem_transactions
    FOR EACH ROW EXECUTE FUNCTION protect_save_metadata();
CREATE TRIGGER gem_transactions_no_truncate BEFORE TRUNCATE ON gem_transactions
    EXECUTE FUNCTION reject_save_truncate();
