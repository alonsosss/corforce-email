#!/bin/bash

source /source_env.sh

MAX_AGE=$(redis-cli --raw -h ${MAIL_REDIS_HOST} -a ${MAIL_REDIS_PASSWORD} --no-auth-warning GET Q_MAX_AGE)

if [[ -z ${MAX_AGE} ]]; then
  echo "Max age for quarantine items not defined"
  exit 1
fi

NUM_REGEXP='^[0-9]+$'
if ! [[ ${MAX_AGE} =~ ${NUM_REGEXP} ]] ; then
  echo "Max age for quarantine items invalid"
  exit 1
fi

PSQL="psql -X -q -At -h ${MAIL_DB_HOST} -p ${MAIL_DB_PORT:-5432} -U ${MAIL_DB_USER} -d ${MAIL_DB_NAME}"
export PGPASSWORD="${MAIL_DB_PASSWORD}"

# La tabla de cuarentena la creara el servicio mail-security; hasta entonces esta tarea no hace nada.
if [[ -z $(${PSQL} -c "SELECT to_regclass('mail.quarantine')") ]]; then
  echo "Quarantine table mail.quarantine does not exist yet, nothing to clean"
  exit 0
fi

TO_DELETE=$(${PSQL} -c "SELECT COUNT(id) FROM mail.quarantine WHERE created_at < now() - make_interval(days => ${MAX_AGE//[!0-9]/})")
${PSQL} -c "DELETE FROM mail.quarantine WHERE created_at < now() - make_interval(days => ${MAX_AGE//[!0-9]/})"
echo "Deleted ${TO_DELETE} items from quarantine table (max age is ${MAX_AGE//[!0-9]/} days)"
