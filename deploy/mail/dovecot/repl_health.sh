#!/bin/bash

source /source_env.sh

# Do not attempt to write to slave
if [[ ! -z ${REDIS_SLAVEOF_IP} ]]; then
  REDIS_CMDLINE="redis-cli -h ${REDIS_SLAVEOF_IP} -p ${REDIS_SLAVEOF_PORT} -a ${MAIL_REDIS_PASSWORD} --no-auth-warning"
else
  REDIS_CMDLINE="redis-cli -h ${MAIL_REDIS_HOST} -p 6379 -a ${MAIL_REDIS_PASSWORD} --no-auth-warning"
fi

# Is replication active?
# grep on file is less expensive than doveconf
if [ -n ${MAIL_REPLICA_IP} ]; then
  ${REDIS_CMDLINE} SET DOVECOT_REPL_HEALTH 1 > /dev/null
  exit
fi

FAILED_SYNCS=$(doveadm replicator status | grep "Waiting 'failed' requests" | grep -oE '[0-9]+')

# Set amount of failed jobs as DOVECOT_REPL_HEALTH
# 1 failed job for platform.local (master user) is expected and healthy
if [[ "${FAILED_SYNCS}" != 0 ]] && [[ "${FAILED_SYNCS}" != 1 ]]; then
  printf "Dovecot replicator has %d failed jobs\n" "${FAILED_SYNCS}"
  ${REDIS_CMDLINE} SET DOVECOT_REPL_HEALTH "${FAILED_SYNCS}" > /dev/null
else
  ${REDIS_CMDLINE} SET DOVECOT_REPL_HEALTH 1 > /dev/null
fi
