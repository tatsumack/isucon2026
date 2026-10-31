-- 画像の所有者と種類を id だけで引くための、パーティションしていない索引用テーブル。
-- image_objects は created_at で36個にパーティションされ、主キーが (id, created_at) のため、
-- id だけの検索ではパーティションを絞れず、全パーティションの主キーを探索していた (1回約1.5ms)。
-- 行は image_objects へのINSERT / DELETE のトリガーで同期する。アプリのコードは image_objects にだけ書けばよい。
-- init.sh の /initialize のたびに流す。作成は IF NOT EXISTS、初回の取り込みはテーブルが空のときだけ。
CREATE TABLE IF NOT EXISTS image_owners (
  id varchar(36) PRIMARY KEY,
  owner_user_id varchar(36) NOT NULL,
  media_type text NOT NULL,
  created_at timestamptz NOT NULL
);

INSERT INTO image_owners (id, owner_user_id, media_type, created_at)
SELECT id, owner_user_id, media_type, created_at FROM image_objects
WHERE NOT EXISTS (SELECT 1 FROM image_owners)
ON CONFLICT (id) DO NOTHING;

CREATE OR REPLACE FUNCTION image_owners_sync() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    INSERT INTO image_owners (id, owner_user_id, media_type, created_at)
    VALUES (NEW.id, NEW.owner_user_id, NEW.media_type, NEW.created_at)
    ON CONFLICT (id) DO NOTHING;
  ELSE
    DELETE FROM image_owners WHERE id = OLD.id;
  END IF;
  RETURN NULL;
END
$$;

CREATE OR REPLACE TRIGGER image_owners_sync
  AFTER INSERT OR DELETE ON image_objects
  FOR EACH ROW EXECUTE FUNCTION image_owners_sync();
