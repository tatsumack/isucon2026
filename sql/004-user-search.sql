-- ユーザー検索用に、Goの normalizeSearchText で正規化した username / display_name を持つ。
-- 値は /initialize (refreshUserSearchColumns) と、登録・表示名の変更で入れる。
ALTER TABLE users
  ADD COLUMN IF NOT EXISTS search_username text,
  ADD COLUMN IF NOT EXISTS search_display_name text;
