-- ユーザー検索の LIKE '%q%' (search_username / search_display_name) に使うトライグラムの GIN インデックス。
-- 検索は status = 'active' の利用者だけを対象にするので、部分インデックスにする。
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE INDEX IF NOT EXISTS users_active_search_username_trgm
  ON users USING gin (search_username gin_trgm_ops)
  WHERE status = 'active';
CREATE INDEX IF NOT EXISTS users_active_search_display_name_trgm
  ON users USING gin (search_display_name gin_trgm_ops)
  WHERE status = 'active';
