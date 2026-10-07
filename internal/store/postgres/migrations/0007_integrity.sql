-- Session generations fence already-admitted writes during login/import.
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS session_token TEXT NOT NULL DEFAULT '';
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS session_generation BIGINT NOT NULL DEFAULT 0;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS session_revoked BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS session_history JSONB NOT NULL DEFAULT '{}';
UPDATE credentials SET verification_sent_at=now() WHERE verification_token IS NOT NULL AND verification_sent_at IS NULL;

-- Retry results are committed in the same transaction as economy changes.
CREATE TABLE IF NOT EXISTS request_results (
    user_id BIGINT NOT NULL,
    generation BIGINT NOT NULL,
    request_key TEXT NOT NULL,
    digest TEXT NOT NULL,
    response JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, generation, request_key)
);

-- Keep legacy bad balances visible for repair, while rejecting new invalid writes.
ALTER TABLE gems ADD CONSTRAINT gems_valid_balance CHECK
    (free >= 0 AND paid >= 0 AND free::numeric + paid::numeric <= 9223372036854775807) NOT VALID;
