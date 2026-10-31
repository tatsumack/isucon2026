from harness import *
run('schema_reset',expand(R/'sql/001-schema.sql'))
sql="""INSERT INTO users(id,username,secret_key_hash,display_name,region,status,created_at) VALUES
 ('target','t','x','T','asia','active','2026-10-01'),
 ('inactive','i','x','I','asia','deleted','2026-10-01'),
 ('other','o','x','O','europe','active','2026-10-01'),
 ('日本:語','j','x','J','america','active','2026-10-01'),
 ('quote"id','q','x','Q','asia','active','2026-10-01');
INSERT INTO posting_windows(id,region,local_date,starts_at,on_time_deadline,created_at) VALUES
 ('legacy','asia','2026-10-01','2026-10-01 12:00Z','2026-10-01 12:02Z','2026-10-01');
INSERT INTO notifications(id,user_id,posting_window_id,created_at) VALUES ('legacy-id','target','legacy','2026-10-01 12:00Z');
"""+(R/'sql/003-initial-snapshot.sql').read_text()+expand(R/'sql/reset.sql')
sql+=check("(SELECT ready AND generation=1 FROM notification_history_state WHERE id)",'seed ready generation')
sql+=check("(SELECT count(*) FROM membership)=4 AND NOT EXISTS(SELECT 1 FROM membership WHERE user_id='inactive' OR epoch<>0 OR NOT present)",'active only epoch zero')
sql+=check("NOT EXISTS(SELECT 1 FROM posting_windows WHERE cut_epoch IS NOT NULL) AND (SELECT id FROM notifications)='legacy-id'",'legacy survives seed')
run('initial_seed',sql)
sql="""UPDATE users SET region='europe',status='deleted' WHERE id='target';
UPDATE region_state SET current_epoch=50;
INSERT INTO membership VALUES ('target','asia',50,false),('target','europe',50,true);
INSERT INTO users(id,username,secret_key_hash,display_name,region,created_at) VALUES ('late','l','x','L','asia','2026-11-01');
INSERT INTO membership VALUES ('late','asia',50,true);
UPDATE posting_windows SET ended_at='2026-10-02';
INSERT INTO posting_windows(id,region,local_date,starts_at,on_time_deadline,created_at,cut_epoch) VALUES ('new','asia','2026-11-01','2026-11-01 12:00Z','2026-11-01 12:02Z','2026-11-01',50);
"""+expand(R/'sql/reset.sql')
sql+=check("(SELECT ready AND generation=2 FROM notification_history_state WHERE id)",'reset generation increments')
sql+=check("(SELECT count(*) FROM membership)=4 AND NOT EXISTS(SELECT 1 FROM membership WHERE epoch<>0 OR NOT present OR user_id='late')",'reset reconstructs only baseline')
sql+=check("(SELECT region='asia' AND status='active' FROM users WHERE id='target') AND NOT EXISTS(SELECT 1 FROM users WHERE id='late')",'user restored')
sql+=check("NOT EXISTS(SELECT 1 FROM posting_windows WHERE cut_epoch IS NOT NULL OR id='new') AND NOT EXISTS(SELECT 1 FROM region_state WHERE current_epoch<>0)",'reset epoch window pair')
run('reset_after_mutations',sql)
