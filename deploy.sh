#!/bin/sh
set -ex

# 使い方: ./deploy.sh <branch> [host...]
#   hostを省略すると全台にデプロイする。hostは ~/.ssh/config のHost名
#   asia2はDB (PostgreSQL)、それ以外はアプリ (nginx + Go) としてデプロイする
BRANCH=$1
if [ -z "$BRANCH" ]; then
  echo "you must set branch"
  exit 1
fi
shift
HOSTS=${*:-"asia2 asia1 europe1 america1"}

PROJECT_ROOT=/home/isucon/webapp
APP_DIR=go                     # PROJECT_ROOTからの相対パス
APP_BINARY=isureal             # go buildの出力名 (systemdのExecStartと合わせる)
APP_SERVICE=isureal-go.service
DB_SERVER=asia2

LOG_BACKUP_DIR=/var/log/isucon
BACKUP_TARGET_LIST="/var/log/nginx/access.log /var/log/nginx/error.log"

KEY_OPTION="-A"

# リポジトリを指定ブランチに合わせ、etc/配下の設定ファイルと env.sh を反映する
update_repo() {
  cat <<EOS | ssh $KEY_OPTION $1 sh
set -e
cd $PROJECT_ROOT
git clean -fd
git fetch -p
git checkout -B $BRANCH origin/$BRANCH
git reset --hard origin/$BRANCH
git rev-parse --short HEAD
cp etc/env/$1.sh /home/isucon/env.sh
EOS
}

deploy_db() {
  update_repo $1
  cat <<EOS | ssh $KEY_OPTION $1 sh
set -e
cd $PROJECT_ROOT
sudo cp -r etc/postgresql/. /etc/postgresql/
sudo chown -R postgres:postgres /etc/postgresql/16/main
sudo chmod 640 /etc/postgresql/16/main/pg_hba.conf
sudo systemctl restart postgresql
sudo -u postgres psql -d isureal -X -q -c 'CREATE EXTENSION IF NOT EXISTS pg_stat_statements'
EOS
}

deploy_app() {
  update_repo $1
  LOG_DATE=`date +"%H%M%S"`
  cat <<EOS | ssh $KEY_OPTION $1 sh
set -e
cd $PROJECT_ROOT
hash=\`git rev-parse --short HEAD\`
sudo cp -r etc/nginx/. /etc/nginx/
sudo nginx -t
sudo cp etc/systemd/*.service /etc/systemd/system/
sudo systemctl daemon-reload
# 以前のアプリが 0600 で書いた画像を nginx (www-data) が X-Accel-Redirect で読めるようにする
if [ -d /home/isucon/images ]; then find /home/isucon/images -type f ! -perm -044 -exec chmod go+r {} +; fi
cd $APP_DIR
PATH=/home/isucon/local/go/bin:/usr/local/go/bin:/home/isucon/go/bin:/usr/bin go build -o $APP_BINARY ./cmd/isureal
sudo mkdir -p $LOG_BACKUP_DIR
for LOG_PATH in $BACKUP_TARGET_LIST; do
  if [ -f \$LOG_PATH ]; then sudo mv \$LOG_PATH $LOG_BACKUP_DIR/\`basename \$LOG_PATH\`_${LOG_DATE}_\$hash; fi
done
sudo systemctl restart $APP_SERVICE
sudo systemctl restart nginx
EOS
}

for HOST in $HOSTS
do
  echo "Deploy $BRANCH to $HOST"
  if [ "$HOST" = "$DB_SERVER" ]; then
    deploy_db $HOST
  else
    deploy_app $HOST
  fi
done
echo "Deploy script finished."
