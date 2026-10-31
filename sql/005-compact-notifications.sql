-- Exact immutable recipients; arbitrary existing varchar user IDs remain keys.
CREATE TABLE IF NOT EXISTS notification_recipient_snapshots (
  posting_window_id varchar(36) PRIMARY KEY REFERENCES posting_windows(id) ON DELETE RESTRICT,
  recipients jsonb NOT NULL CHECK (jsonb_typeof(recipients) = 'object')
);

-- UUIDv8-shaped, namespace-separated SHA-256 name (122 retained digest bits).
-- This is an identifier, not an authentication token. The user byte-length prefix
-- makes concatenation unambiguous even for non-UUID/case-sensitive existing IDs.
CREATE OR REPLACE FUNCTION compact_notification_id(user_id text, window_id text)
RETURNS text LANGUAGE SQL IMMUTABLE STRICT PARALLEL SAFE AS $$
  WITH digest AS (
    SELECT substring(sha256(convert_to('isureal:notification:v1:' ||
      octet_length(user_id)::text || ':' || user_id || window_id, 'UTF8')) FROM 1 FOR 16) AS b
  ), formatted AS (
    SELECT encode(set_byte(set_byte(b, 6, (get_byte(b, 6) & 15) | 128),
                                   8, (get_byte(b, 8) & 63) | 128), 'hex') AS h
    FROM digest
  )
  SELECT substr(h,1,8) || '-' || substr(h,9,4) || '-' || substr(h,13,4) ||
         '-' || substr(h,17,4) || '-' || substr(h,21,12) FROM formatted
$$;
