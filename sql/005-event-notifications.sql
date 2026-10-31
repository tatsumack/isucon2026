-- 新方式の導入だけでは既存ユーザーの過去所属は復元しない。
ALTER TABLE posting_windows ADD COLUMN IF NOT EXISTS cut_epoch bigint CHECK (cut_epoch >= 0);
CREATE TABLE IF NOT EXISTS region_state (
  region text PRIMARY KEY CHECK (region IN ('asia', 'europe', 'america')),
  current_epoch bigint NOT NULL CHECK (current_epoch >= 0)
);
INSERT INTO region_state(region, current_epoch) VALUES ('asia', 0), ('europe', 0), ('america', 0)
ON CONFLICT (region) DO NOTHING;
-- FKを付けない: 退会後も履歴を保持し、region_state行ロックで登録を直列化しない。
CREATE TABLE IF NOT EXISTS membership (
  user_id varchar(36) NOT NULL,
  region text NOT NULL CHECK (region IN ('asia', 'europe', 'america')),
  epoch bigint NOT NULL CHECK (epoch >= 0),
  present boolean NOT NULL,
  PRIMARY KEY (user_id, region, epoch)
);
CREATE TABLE IF NOT EXISTS notification_history_state (
  id boolean PRIMARY KEY DEFAULT true CHECK (id),
  ready boolean NOT NULL DEFAULT false,
  generation bigint NOT NULL DEFAULT 0
);
INSERT INTO notification_history_state(id) VALUES (true) ON CONFLICT (id) DO NOTHING;
