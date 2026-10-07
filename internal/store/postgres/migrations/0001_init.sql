-- 0001_init: LilyPad core schema.

-- LilyPad Portal: the durable, web-registered identity (email-keyed).
CREATE TABLE IF NOT EXISTS credentials (
    email                   TEXT PRIMARY KEY,
    password_hash           TEXT        NOT NULL,
    email_verified          BOOLEAN     NOT NULL DEFAULT FALSE,
    verification_token      TEXT,
    verification_sent_at    TIMESTAMPTZ,
    migration_code          TEXT UNIQUE,
    migration_password_hash TEXT,
    xuid                    TEXT UNIQUE NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Sequence backing game userIds (starts high to look plausible).
CREATE SEQUENCE IF NOT EXISTS account_user_id_seq START 1000000;

-- Platform XUID -> game account.
CREATE TABLE IF NOT EXISTS accounts (
    x_uid        TEXT PRIMARY KEY,
    user_id      BIGINT UNIQUE NOT NULL,
    region       TEXT,
    country      TEXT,
    language     TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ
);

-- Per-player, per-table save state.
CREATE TABLE IF NOT EXISTS user_state (
    user_id    BIGINT      NOT NULL,
    table_name TEXT        NOT NULL,
    rows       JSONB       NOT NULL,
    hash       TEXT        NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, table_name)
);
