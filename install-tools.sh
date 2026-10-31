#!/bin/sh
# 計測ツールをアプリサーバに入れる (各サーバで1回だけ実行すればよい)
# 使い方: ./install-tools.sh [host...]
set -ex

ALP_VERSION=v1.0.21
HOSTS=${*:-"asia1 europe1 america1"}

for HOST in $HOSTS
do
cat <<EOS | ssh $HOST sh
set -e
if [ ! -x /usr/local/bin/alp ]; then
  cd /tmp
  curl -sSL -o alp.tar.gz https://github.com/tkuchiki/alp/releases/download/${ALP_VERSION}/alp_linux_amd64.tar.gz
  tar xzf alp.tar.gz alp
  sudo install alp /usr/local/bin/alp
  rm -f alp alp.tar.gz
fi
/usr/local/bin/alp --version
EOS
done
