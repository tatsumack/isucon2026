-- Authentication lookups run for every authenticated request.
CREATE INDEX IF NOT EXISTS login_sessions_active_token_hash
  ON login_sessions (token_hash)
  WHERE active = true;
CREATE INDEX IF NOT EXISTS login_sessions_active_user_id
  ON login_sessions (user_id)
  WHERE active = true;
CREATE INDEX IF NOT EXISTS administrator_sessions_active_token_hash
  ON administrator_sessions (token_hash)
  WHERE active = true;

-- Usernames are used by registration, login, and user-list pagination.
CREATE INDEX IF NOT EXISTS users_username
  ON users (username);
CREATE INDEX IF NOT EXISTS users_active_username_id
  ON users (username, id)
  WHERE status = 'active';
CREATE INDEX IF NOT EXISTS administrators_username
  ON administrators (username);

-- Feed, post-history, and image-validity lookups.
CREATE INDEX IF NOT EXISTS posts_active_posted_at_id
  ON posts (posted_at DESC, id DESC)
  WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS posts_active_author_window
  ON posts (author_id, posting_window_id)
  WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS posts_active_image_id
  ON posts (image_id)
  WHERE deleted_at IS NULL;

-- The existing unique indexes cover the forward direction. These indexes
-- cover reverse relationship lookups and pending-request lists.
CREATE INDEX IF NOT EXISTS friendships_user_high_low
  ON friendships (user_high_id, user_low_id);
CREATE INDEX IF NOT EXISTS follows_followee_follower
  ON follows (followee_id, follower_id);
CREATE INDEX IF NOT EXISTS block_relations_blocked_blocker
  ON block_relations (blocked_id, blocker_id);
CREATE INDEX IF NOT EXISTS friend_requests_pending_recipient_updated
  ON friend_requests (recipient_id, updated_at DESC, id DESC)
  WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS friend_requests_pending_requester_updated
  ON friend_requests (requester_id, updated_at DESC, id DESC)
  WHERE status = 'pending';

-- Current interactions are read for feeds, details, memories, and images.
CREATE INDEX IF NOT EXISTS realmojis_current_post_created
  ON realmojis (post_id, created_at DESC, id DESC)
  WHERE replaced_at IS NULL AND deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS realmojis_current_image_id
  ON realmojis (image_id)
  WHERE replaced_at IS NULL AND deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS comments_active_post_created
  ON comments (post_id, created_at, id)
  WHERE deleted_at IS NULL;

-- SSE notification polling filters and orders by these columns.
CREATE INDEX IF NOT EXISTS notifications_user_created_id
  ON notifications (user_id, created_at, id);
