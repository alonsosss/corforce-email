#!/bin/bash

# Reading container IDs
# Wrapping as array to ensure trimmed content when calling $DOVECOT etc.
DOVECOT=($(curl --silent --insecure https://dockerapi.mail-engines/containers/json | jq -r ".[] | {name: .Config.Labels[\"com.docker.compose.service\"], project: .Config.Labels[\"com.docker.compose.project\"], id: .Id}" | jq -rc "select( .name | tostring | contains(\"dovecot-mail\")) | select( .project | tostring | contains(\"${COMPOSE_PROJECT_NAME,,}\")) | .id" | tr "\n" " "))
POSTFIX=($(curl --silent --insecure https://dockerapi.mail-engines/containers/json | jq -r ".[] | {name: .Config.Labels[\"com.docker.compose.service\"], project: .Config.Labels[\"com.docker.compose.project\"], id: .Id}" | jq -rc "select( .name | tostring | contains(\"postfix-mail\")) | select( .project | tostring | contains(\"${COMPOSE_PROJECT_NAME,,}\")) | .id" | tr "\n" " "))

reload_dovecot(){
  echo "Reloading Dovecot..."
  DOVECOT_RELOAD_RET=$(curl -X POST --insecure https://dockerapi.mail-engines/containers/${DOVECOT}/exec -d '{"cmd":"reload", "task":"dovecot"}' --silent -H 'Content-type: application/json' | jq -r .type)
  [[ ${DOVECOT_RELOAD_RET} != 'success' ]] && { echo "Could not reload Dovecot, restarting container..."; restart_container ${DOVECOT} ; }
}

reload_postfix(){
  echo "Reloading Postfix..."
  POSTFIX_RELOAD_RET=$(curl -X POST --insecure https://dockerapi.mail-engines/containers/${POSTFIX}/exec -d '{"cmd":"reload", "task":"postfix"}' --silent -H 'Content-type: application/json' | jq -r .type)
  [[ ${POSTFIX_RELOAD_RET} != 'success' ]] && { echo "Could not reload Postfix, restarting container..."; restart_container ${POSTFIX} ; }
}

restart_container(){
  for container in $*; do
    echo "Restarting ${container}..."
    C_REST_OUT=$(curl -X POST --insecure https://dockerapi.mail-engines/containers/${container}/restart --silent | jq -r '.msg')
    echo "${C_REST_OUT}"
  done
}

if [[ "${CERT_AMOUNT_CHANGED}" == "1" ]]; then
  restart_container ${DOVECOT}
  restart_container ${POSTFIX}
else
  #reload_dovecot
  restart_container ${DOVECOT}
  #reload_postfix
  restart_container ${POSTFIX}
fi
