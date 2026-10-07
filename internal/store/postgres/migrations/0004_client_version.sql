-- 0004_client_version: durable "latest seen" installed-build version, reported
-- by clientpatch at game launch. A single row (id=1) holds the most recent
-- report; it is the /api/app/start fallback for clients that carry no
-- x-clientpatch-id header (proxies, non-clientpatch clients), and it survives
-- a server restart.
CREATE TABLE IF NOT EXISTS client_version (
    id              SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    program_version TEXT        NOT NULL DEFAULT '',
    asset_version   TEXT        NOT NULL DEFAULT '',
    asset_hash      TEXT        NOT NULL DEFAULT '',
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO client_version (id) VALUES (1) ON CONFLICT (id) DO NOTHING;
