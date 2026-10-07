-- Repair the neutral seed's zero registration time from the durable account
-- creation record. Preserve imported nonzero dates and every other profile field.
-- Existing state triggers retain before/after recovery data and rotate its token;
-- the migration runner fences all pre-upgrade game processes and queued writes.
UPDATE user_state s
SET rows = jsonb_set(s.rows, '{0,_registeredAt}',
                     to_jsonb(floor(extract(epoch FROM a.created_at))::bigint)),
    updated_at = now()
FROM accounts a
WHERE a.user_id = s.user_id
  AND s.table_name = 'user_profile'
  AND CASE WHEN jsonb_typeof(s.rows) = 'array' THEN jsonb_array_length(s.rows) = 1 ELSE false END
  AND jsonb_typeof(s.rows->0) = 'object'
  AND s.rows->0->'_registeredAt' = '0'::jsonb
  AND a.created_at >= to_timestamp(1);
