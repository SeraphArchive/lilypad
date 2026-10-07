-- 0003_requestor_bindings: durable platform requestor bindings.

-- The Gree GameLib xoauth_requestor_id is the client/device handle sent to
-- /v1.0/auth/x_uid after migration/code/verify. Keep the established XUID in
-- the database so a server restart does not force the player through reset and
-- registration again.
CREATE TABLE IF NOT EXISTS platform_requestor_bindings (
    requestor_id TEXT PRIMARY KEY,
    x_uid        TEXT        NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
