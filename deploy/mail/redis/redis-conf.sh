#!/bin/sh

cat <<EOF > /redis.conf
requirepass $MAIL_REDIS_PASSWORD
user quota_notify on nopass ~QW_* -@all +get +hget +ping
EOF

if [ -n "$MAIL_REDIS_MASTER_PASSWORD" ]; then
  echo "masterauth $MAIL_REDIS_MASTER_PASSWORD" >> /redis.conf
fi

exec redis-server /redis.conf
