SET session_replication_role = replica;

BEGIN;

-- initialize は通常リクエストを停止した状態で実行する。既存transactionは完了を待つ。
-- user -> region -> window の通常更新と同時実行してresetを運用しない。
LOCK TABLE users, posting_windows, membership, region_state, notification_history_state IN ACCESS EXCLUSIVE MODE;

TRUNCATE ip_bans, login_sessions, administrator_sessions, friendships, friend_requests, follows, block_relations;

DELETE FROM notifications WHERE created_at > (SELECT created_at FROM initial_data_boundary);
DELETE FROM comments WHERE created_at > (SELECT created_at FROM initial_data_boundary);
DELETE FROM realmojis WHERE created_at > (SELECT created_at FROM initial_data_boundary);
DELETE FROM posts WHERE created_at > (SELECT created_at FROM initial_data_boundary);
DELETE FROM posting_windows WHERE created_at > (SELECT created_at FROM initial_data_boundary);
DO $$
DECLARE partition_name text;
BEGIN
  FOR partition_name IN
    SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
    WHERE i.inhparent = 'image_objects'::regclass
      AND c.relname > 'image_objects_' || to_char((SELECT created_at FROM initial_data_boundary) AT TIME ZONE 'UTC', 'YYYYMM')
  LOOP
    EXECUTE format('TRUNCATE %I', partition_name);
  END LOOP;
END $$;
DELETE FROM image_objects WHERE created_at > (SELECT created_at FROM initial_data_boundary);
DELETE FROM users WHERE created_at > (SELECT created_at FROM initial_data_boundary);

UPDATE users u
SET display_name = i.display_name, bio = i.bio, profile_image_id = i.profile_image_id, region = i.region,
    account_type = i.account_type, status = i.status, deleted_at = i.deleted_at, updated_at = i.updated_at
FROM initial_users i
WHERE u.id = i.id
  AND (u.display_name, u.bio, u.profile_image_id, u.region, u.account_type, u.status, u.deleted_at, u.updated_at)
      IS DISTINCT FROM (i.display_name, i.bio, i.profile_image_id, i.region, i.account_type, i.status, i.deleted_at, i.updated_at);

INSERT INTO friend_requests (id, requester_id, recipient_id, status, created_at, updated_at)
SELECT id, requester_id, recipient_id, status, created_at, updated_at FROM initial_friend_requests;
INSERT INTO friendships (id, user_low_id, user_high_id, source_request_id, created_at, updated_at)
SELECT id, user_low_id, user_high_id, source_request_id, created_at, updated_at FROM initial_friendships;
INSERT INTO follows (id, follower_id, followee_id, created_at, updated_at)
SELECT id, follower_id, followee_id, created_at, updated_at FROM initial_follows;
INSERT INTO block_relations (id, blocker_id, blocked_id, created_at)
SELECT id, blocker_id, blocked_id, created_at FROM initial_block_relations;

-- 初期windowはlegacy通知だけを使う。現在所属から過去通知を再生成しない。
UPDATE posting_windows SET cut_epoch = NULL;
TRUNCATE membership;
UPDATE region_state SET current_epoch = 0;
INSERT INTO membership(user_id, region, epoch, present)
SELECT id, region, 0, true FROM users WHERE status = 'active';
UPDATE notification_history_state SET ready = true, generation = generation + 1 WHERE id = true;

COMMIT;
