-- 0005_session_base: concurrent-login invalidation. The client's x-msgid header
-- is a per-process counter initialized to the current unix time and incremented
-- per request, so the newest session's ids dominate any older session's. The
-- login routes (user/client/id, user/migration/id) record the session's base
-- msgid here; requests carrying an older msgid are answered 401 (the official
-- "logged in on another device" signal).
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS session_base BIGINT NOT NULL DEFAULT 0;
