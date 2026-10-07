-- Opaque table versions survive process restarts and change even on A -> B -> A.
-- Deleted tables retain a token so confirm/pull can clear an old local copy.
CREATE SEQUENCE save_revision_seq;
ALTER TABLE user_state ADD COLUMN save_revision BIGINT NOT NULL DEFAULT 0;
ALTER TABLE accounts ADD COLUMN write_barrier BIGINT NOT NULL DEFAULT 0;
ALTER TABLE accounts ADD COLUMN retired_sessions JSONB NOT NULL DEFAULT '{}';
CREATE TABLE user_state_tokens (
    user_id BIGINT NOT NULL,
    table_name TEXT NOT NULL,
    revision BIGINT NOT NULL,
    hash TEXT NOT NULL,
    PRIMARY KEY (user_id, table_name)
);

-- No automatic expiry: a code upgrade/import/external edit must not discard
-- the only recoverable copy. History is committed with the actual mutation.
CREATE TABLE player_data_history (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL,
    table_name TEXT NOT NULL,
    operation TEXT NOT NULL,
    old_data JSONB,
    new_data JSONB,
    old_hash TEXT,
    new_hash TEXT,
    source TEXT NOT NULL,
    transaction_id xid8 NOT NULL DEFAULT pg_current_xact_id(),
    changed_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX player_data_history_player ON player_data_history(user_id, id);
CREATE INDEX request_results_operation ON request_results(user_id,request_key,generation DESC);

-- Preserve every pre-upgrade table before changing only its token.
INSERT INTO player_data_history(user_id,table_name,operation,old_data,new_data,old_hash,source)
SELECT user_id,table_name,'UPGRADE',rows,rows,hash,'migration' FROM user_state;
INSERT INTO user_state_tokens(user_id,table_name,revision,hash)
SELECT user_id,table_name,nextval('save_revision_seq'),'' FROM user_state;
UPDATE user_state_tokens t SET hash=CASE
    WHEN t.table_name IN ('user_reward_group_log','user_web_shop_product') THEN ''
    ELSE md5(s.rows::text || ':' || t.revision::text) END
FROM user_state s WHERE s.user_id=t.user_id AND s.table_name=t.table_name;
UPDATE user_state s SET hash=t.hash,save_revision=t.revision
FROM user_state_tokens t WHERE s.user_id=t.user_id AND s.table_name=t.table_name;

-- In-flight requests from the old implementation must re-login and pull.
UPDATE accounts SET session_revoked=TRUE,session_generation=session_generation+1,
    retired_sessions=retired_sessions || session_history || CASE WHEN session_token<>'' THEN jsonb_build_object(session_token,0) ELSE '{}'::jsonb END,
    write_barrier=GREATEST(write_barrier,floor(extract(epoch FROM clock_timestamp()))::bigint);

-- Only assign a version in BEFORE. History/token side effects belong to AFTER:
-- INSERT ... ON CONFLICT DO NOTHING must not change an existing save's token.
CREATE FUNCTION prepare_user_state() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE uid BIGINT; version BIGINT;
BEGIN
    IF TG_OP='UPDATE' AND (NEW.user_id<>OLD.user_id OR NEW.table_name<>OLD.table_name) THEN
        RAISE EXCEPTION 'save identity cannot be changed in place; use delete/insert';
    END IF;
    IF TG_OP='DELETE' THEN uid:=OLD.user_id; ELSE uid:=NEW.user_id; END IF;
    PERFORM pg_advisory_xact_lock(uid);
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    IF TG_OP='UPDATE' AND NEW.rows=OLD.rows AND NEW.hash=OLD.hash THEN
        NEW.save_revision:=OLD.save_revision;
        RETURN NEW;
    END IF;
    version:=nextval('save_revision_seq');
    NEW.save_revision:=version;
    NEW.hash:=CASE WHEN NEW.table_name IN ('user_reward_group_log','user_web_shop_product') THEN ''
                  ELSE md5(NEW.rows::text || ':' || version::text) END;
    RETURN NEW;
END $$;
CREATE TRIGGER user_state_version BEFORE INSERT OR UPDATE OR DELETE ON user_state
FOR EACH ROW EXECUTE FUNCTION prepare_user_state();

CREATE FUNCTION protect_user_state() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    uid BIGINT;
    name TEXT;
    version BIGINT;
    token TEXT;
    before_rows JSONB;
    after_rows JSONB;
    before_hash TEXT;
    origin TEXT;
BEGIN
    IF TG_OP='UPDATE' AND (NEW.user_id<>OLD.user_id OR NEW.table_name<>OLD.table_name) THEN
        RAISE EXCEPTION 'save identity cannot be changed in place; use delete/insert';
    END IF;
    IF TG_OP='DELETE' THEN uid:=OLD.user_id; name:=OLD.table_name;
    ELSE uid:=NEW.user_id; name:=NEW.table_name; END IF;
    PERFORM pg_advisory_xact_lock(uid);
    IF TG_OP='UPDATE' AND NEW.rows=OLD.rows AND NEW.hash=OLD.hash THEN RETURN NULL; END IF;
    IF TG_OP<>'INSERT' THEN before_rows:=OLD.rows; before_hash:=OLD.hash; END IF;
    IF TG_OP='DELETE' THEN
        after_rows:='[]'::jsonb;
        version:=nextval('save_revision_seq');
        token:=CASE WHEN name IN ('user_reward_group_log','user_web_shop_product') THEN ''
                    ELSE md5(after_rows::text || ':' || version::text) END;
    ELSE after_rows:=NEW.rows; version:=NEW.save_revision; token:=NEW.hash; END IF;
    INSERT INTO user_state_tokens(user_id,table_name,revision,hash) VALUES(uid,name,version,token)
    ON CONFLICT(user_id,table_name) DO UPDATE SET revision=EXCLUDED.revision,hash=EXCLUDED.hash;
    origin:=CASE WHEN current_setting('lilypad.internal_write',true)='on' THEN 'server' ELSE 'external' END;
    INSERT INTO player_data_history(user_id,table_name,operation,old_data,new_data,old_hash,new_hash,source)
    VALUES(uid,name,TG_OP,before_rows,CASE WHEN TG_OP='DELETE' THEN NULL ELSE after_rows END,before_hash,token,origin);
    IF origin='external' THEN
        UPDATE accounts SET session_revoked=TRUE,session_generation=session_generation+1,
            retired_sessions=retired_sessions || session_history || CASE WHEN session_token<>'' THEN jsonb_build_object(session_token,0) ELSE '{}'::jsonb END,
            write_barrier=GREATEST(write_barrier,floor(extract(epoch FROM clock_timestamp()))::bigint) WHERE user_id=uid;
    END IF;
    RETURN NULL;
END $$;
CREATE TRIGGER user_state_protection AFTER INSERT OR UPDATE OR DELETE ON user_state
FOR EACH ROW EXECUTE FUNCTION protect_user_state();

CREATE FUNCTION prepare_gems() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE uid BIGINT;
BEGIN
    IF TG_OP='UPDATE' AND NEW.user_id<>OLD.user_id THEN RAISE EXCEPTION 'wallet identity cannot be changed in place'; END IF;
    IF TG_OP='DELETE' THEN uid:=OLD.user_id; ELSE uid:=NEW.user_id; END IF;
    PERFORM pg_advisory_xact_lock(uid);
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER gems_lock BEFORE INSERT OR UPDATE OR DELETE ON gems FOR EACH ROW EXECUTE FUNCTION prepare_gems();

CREATE FUNCTION protect_gems() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE uid BIGINT; before_balance JSONB; after_balance JSONB; origin TEXT;
BEGIN
    IF TG_OP='UPDATE' AND NEW.user_id<>OLD.user_id THEN RAISE EXCEPTION 'wallet identity cannot be changed in place'; END IF;
    IF TG_OP='DELETE' THEN uid:=OLD.user_id; ELSE uid:=NEW.user_id; END IF;
    PERFORM pg_advisory_xact_lock(uid);
    IF TG_OP='UPDATE' AND NEW.free=OLD.free AND NEW.paid=OLD.paid THEN RETURN NULL; END IF;
    IF TG_OP<>'INSERT' THEN before_balance:=jsonb_build_object('free',OLD.free,'paid',OLD.paid); END IF;
    IF TG_OP<>'DELETE' THEN after_balance:=jsonb_build_object('free',NEW.free,'paid',NEW.paid); END IF;
    origin:=CASE WHEN current_setting('lilypad.internal_write',true)='on' THEN 'server' ELSE 'external' END;
    INSERT INTO player_data_history(user_id,table_name,operation,old_data,new_data,source)
    VALUES(uid,'@gems',TG_OP,before_balance,after_balance,origin);
    IF origin='external' THEN UPDATE accounts SET session_revoked=TRUE,session_generation=session_generation+1,
        retired_sessions=retired_sessions || session_history || CASE WHEN session_token<>'' THEN jsonb_build_object(session_token,0) ELSE '{}'::jsonb END,
        write_barrier=GREATEST(write_barrier,floor(extract(epoch FROM clock_timestamp()))::bigint) WHERE user_id=uid; END IF;
    RETURN NULL;
END $$;
CREATE TRIGGER gems_protection AFTER INSERT OR UPDATE OR DELETE ON gems
FOR EACH ROW EXECUTE FUNCTION protect_gems();

-- TRUNCATE bypasses row history and tombstones. DELETE remains available and
-- archives each row, including direct SQL maintenance outside the server.
CREATE FUNCTION reject_save_truncate() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'TRUNCATE bypasses save recovery; use DELETE instead'; END $$;
CREATE TRIGGER user_state_no_truncate BEFORE TRUNCATE ON user_state EXECUTE FUNCTION reject_save_truncate();
CREATE TRIGGER gems_no_truncate BEFORE TRUNCATE ON gems EXECUTE FUNCTION reject_save_truncate();

-- Renaming/deleting the identity would make intact rows look like a new save
-- on next GetOrCreate. Transfer data explicitly instead of orphaning it.
CREATE FUNCTION protect_account_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='UPDATE' THEN
        IF NEW.user_id<>OLD.user_id OR NEW.x_uid<>OLD.x_uid THEN RAISE EXCEPTION 'game account identity is immutable'; END IF;
        RETURN NEW;
    END IF;
    PERFORM pg_advisory_xact_lock(OLD.user_id);
    IF EXISTS(SELECT 1 FROM user_state_tokens WHERE user_id=OLD.user_id)
       OR EXISTS(SELECT 1 FROM user_state WHERE user_id=OLD.user_id)
       OR EXISTS(SELECT 1 FROM gems WHERE user_id=OLD.user_id)
       OR EXISTS(SELECT 1 FROM player_data_history WHERE user_id=OLD.user_id) THEN
        RAISE EXCEPTION 'account has saved/recoverable data; identity deletion would orphan it';
    END IF;
    RETURN OLD;
END $$;
CREATE TRIGGER accounts_identity BEFORE UPDATE OR DELETE ON accounts FOR EACH ROW EXECUTE FUNCTION protect_account_identity();
CREATE TRIGGER accounts_no_truncate BEFORE TRUNCATE ON accounts EXECUTE FUNCTION reject_save_truncate();

-- Editors change user_state/gems, not the version ledger or recovery copies.
-- A missing token must never make an existing save look like an empty table.
CREATE FUNCTION protect_save_metadata() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF pg_trigger_depth()<=1 AND current_setting('lilypad.internal_write',true) IS DISTINCT FROM 'on' THEN
        RAISE EXCEPTION 'save metadata is managed by the server; edit user_state/gems instead';
    END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER tokens_managed BEFORE INSERT OR UPDATE OR DELETE ON user_state_tokens FOR EACH ROW EXECUTE FUNCTION protect_save_metadata();
CREATE TRIGGER history_managed BEFORE INSERT OR UPDATE OR DELETE ON player_data_history FOR EACH ROW EXECUTE FUNCTION protect_save_metadata();
CREATE TRIGGER tokens_no_truncate BEFORE TRUNCATE ON user_state_tokens EXECUTE FUNCTION reject_save_truncate();
CREATE TRIGGER history_no_truncate BEFORE TRUNCATE ON player_data_history EXECUTE FUNCTION reject_save_truncate();
