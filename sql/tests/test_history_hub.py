from harness import *
import datetime,hashlib
src=(R/'go/posting_window_lock.go').read_text();upsert=re.search(r'tx.ExecContext\(ctx, `(\s*INSERT INTO membership[\s\S]*?)`',src)[1]
assert '$4' not in upsert and 'FROM region_state' in upsert, 'expected post-lock INSERT SELECT membership helper'
query=re.search(r'const notificationPollQuery = `([\s\S]*?)`',(R/'go/notification_hub.go').read_text())[1]
epoch={'asia':0,'europe':0,'america':0};users={'target':('asia',True),'inactive':('asia',False),'other':('europe',True),'日本:語':('america',True),'quote"id':('asia',True)};windows={};sql='SET session_replication_role = origin;\nUPDATE posting_windows SET ended_at=\'2026-10-02\';\n'
def member(user,region,present):
 q=upsert
 for k,v in [('$1',lit(user)),('$2',lit(region)),('$3',str(present).lower())]:q=q.replace(k,v)
 return "SELECT pg_advisory_xact_lock_shared(hashtext('posting_window:' || "+lit(region)+"));\n\n"+q+';\n'
def verify():
 global sql
 for w,(r,c,expected,ended) in windows.items():
  for u in users:
   expr=f"COALESCE((SELECT present FROM membership WHERE user_id={lit(u)} AND region={lit(r)} AND epoch<={c} ORDER BY epoch DESC LIMIT 1),false) = {str(u in expected).lower()}"
   sql+=check(expr,'frozen '+w+' '+u.replace("'",''))
def move(u,r,active=True):
 global sql
 old,was=users[u];sql+='BEGIN ISOLATION LEVEL READ COMMITTED;\n\nSELECT id FROM users WHERE id='+lit(u)+' FOR UPDATE;\n\n'
 for lock in sorted(set([old,r])):sql+="SELECT pg_advisory_xact_lock_shared(hashtext('posting_window:' || "+lit(lock)+"));\n\n"
 sql+='UPDATE users SET region='+lit(r)+',status='+lit('active' if active else 'deleted')+' WHERE id='+lit(u)+';\n'
 if old!=r:sql+=member(u,old,False)
 sql+=member(u,r,active)+'COMMIT;\n\n';users[u]=(r,active);verify()
def opening(w,r):
 global sql
 for old,v in list(windows.items()):
  if v[0]==r and not v[3]:windows[old]=(*v[:3],True)
 c=epoch[r];sql+='BEGIN ISOLATION LEVEL READ COMMITTED;\n\n'+"SELECT pg_advisory_xact_lock(hashtext('posting_window:' || "+lit(r)+"));\n\n"
 sql+='SELECT current_epoch FROM region_state WHERE region='+lit(r)+';\n\n'
 sql+="UPDATE posting_windows SET ended_at='2026-10-31 12:00Z' WHERE region="+lit(r)+' AND ended_at IS NULL;\n'
 sql+=f"INSERT INTO posting_windows(id,region,local_date,starts_at,on_time_deadline,cut_epoch) VALUES ({lit(w)},{lit(r)},'2026-11-{c+1:02d}','2026-10-31 12:00Z','2026-10-31 12:02Z',{c});\n"
 sql+='UPDATE region_state SET current_epoch=current_epoch+1 WHERE region='+lit(r)+';\nCOMMIT;\n\n';epoch[r]+=1
 windows[w]=(r,c,{u for u,(ur,a) in users.items() if ur==r and a},False);verify()
move('target','europe');move('target','asia') # same epoch A B A
opening('wa0','asia')
sql+="BEGIN; INSERT INTO users(id,username,secret_key_hash,display_name,region) VALUES('late','late','x','Late','asia');\n"+member('late','asia',True)+'COMMIT;\n\n';users['late']=('asia',True);verify()
move('target','europe');opening('we0','europe');opening('wm0','america')
move('target','europe',False);opening('we1','europe') # false predecessor must win
move('target','asia');opening('wa1','asia')
# both writer rollback and admin epoch rollback must leave history and cut immutable
sql+='BEGIN;\n'+member('other','europe',False)+"UPDATE users SET status='deleted' WHERE id='other'; ROLLBACK;\n\n"
sql+="BEGIN; SELECT pg_advisory_xact_lock(hashtext('posting_window:asia')); UPDATE posting_windows SET ended_at='2026-10-31 13:00Z' WHERE region='asia' AND ended_at IS NULL; INSERT INTO posting_windows(id,region,local_date,starts_at,on_time_deadline,cut_epoch) VALUES('aborted-window','asia','2026-12-01','2026-10-31 13:00Z','2026-10-31 13:02Z',2); UPDATE region_state SET current_epoch=current_epoch+1 WHERE region='asia'; ROLLBACK;\n\n"
sql+=check("NOT EXISTS(SELECT 1 FROM posting_windows WHERE id='aborted-window') AND (SELECT ended_at IS NULL FROM posting_windows WHERE id='wa1')",'admin full rollback')
verify();sql+=check("(SELECT current_epoch FROM region_state WHERE region='asia')=2",'admin epoch rollback')
# Fresh-user rollback must not leak history
sql+="BEGIN; INSERT INTO users(id,username,secret_key_hash,display_name,region) VALUES('abort','abort','x','A','asia');\n"+member('abort','asia',True)+'ROLLBACK;\n\n'+check("NOT EXISTS(SELECT 1 FROM membership WHERE user_id='abort') AND NOT EXISTS(SELECT 1 FROM users WHERE id='abort')",'register rollback')
# Same user belongs to all three still-active cuts; equal-time selection crosses regions.
move('target','europe');opening('we2','europe')
move('target','america');opening('wm1','america')
# Restore target in Europe after all windows: historical membership must survive current mismatch
move('target','europe',False)
# A legacy duplicate is authoritative, including created_at earlier than the virtual window
sql+="INSERT INTO notifications(id,user_id,posting_window_id,created_at) VALUES ('zz-legacy','target','wa1','2026-10-31 11:59:30Z');\n"
subs=[(i,u,'2026-10-31 11:59Z') for i,u in enumerate(users,1)]+[(90,'target','2026-10-31 12:00Z'),(91,'missing','2026-10-31 11:59Z'),(92,'target','2026-10-31 11:59:45Z')]
values=','.join(f'({i}::bigint,{lit(u)}::text,{lit(t)}::timestamptz)' for i,u,t in subs)
sql+='CREATE TEMP VIEW fetched AS '+query.replace('%s',values)+';\n'
sql+='CREATE TEMP TABLE expected_candidates(user_id text,id text,posting_window_id text,region text,created_at timestamptz);\n'
for w,(r,c,members,ended) in windows.items():
 if ended:continue
 for u in members:
  h=hashlib.sha256(('isureal:event-notification:v1:'+str(len(u))+':'+u+':'+w).encode()).hexdigest();ts=f"{int(datetime.datetime(2026,10,31,12,tzinfo=datetime.timezone.utc).timestamp()*1000):012x}"
  ident=ts[:8]+'-'+ts[8:]+'-7'+h[:3]+'-'+'89ab'[int(h[3],16)&3]+h[4:7]+'-'+h[7:19];at='2026-10-31 12:00Z'
  if (u,w)==('target','wa1'):ident='zz-legacy';at='2026-10-31 11:59:30Z'
  sql+='INSERT INTO expected_candidates VALUES('+','.join(map(lit,[u,ident,w,r,at]))+');\n'
for i,u,t in subs:
 sql+=check(f"(SELECT id FROM fetched WHERE subscription_id={i}) IS NOT DISTINCT FROM (SELECT id FROM expected_candidates WHERE user_id={lit(u)} AND created_at>{lit(t)}::timestamptz ORDER BY created_at,id LIMIT 1)",f'hub oracle subscription{i}')
sql+=check("(SELECT user_region='europe' AND user_status='deleted' AND generation=2 FROM fetched WHERE subscription_id=1)",'current user and generation projection')
sql+=check("NOT EXISTS(SELECT 1 FROM fetched WHERE id IS NOT NULL AND id<>'zz-legacy' AND id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$')",'uuid v7 version variant')
sql+='CREATE TEMP TABLE previous_poll AS SELECT * FROM fetched;\n'+check('NOT EXISTS((SELECT * FROM fetched) EXCEPT ALL (SELECT * FROM previous_poll))','stable repeated poll')
sql+='UPDATE notification_history_state SET generation=generation+1;\n'+check('(SELECT bool_and(generation=3) FROM fetched)','generation invalidation visible')
missing=upsert.replace('$1',lit('no-region-user')).replace('$2',lit('missing-region')).replace('$3','true')
sql+='DO $$ DECLARE affected integer; BEGIN '+missing+'; GET DIAGNOSTICS affected = ROW_COUNT; IF affected <> 0 THEN RAISE EXCEPTION \'missing region inserted history\'; END IF; END $$;\n'
sql+=check("NOT EXISTS(SELECT 1 FROM membership WHERE user_id='no-region-user')",'missing region creates no history')
run('history_and_hub',sql);hashes()
