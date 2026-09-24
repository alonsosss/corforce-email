#!/bin/sh
# Copia el certificado publico que emite acme (volumen mail-ssl, clave 0600 de root) al volumen en memoria
# del relay SMTP con el grupo del relay y 0640, y vuelve a mirar cada SMTP_RELAY_CERT_SYNC_INTERVAL segundos.
# Corre como uid 0 sin capacidades: lee la clave por ser su dueno y los ficheros nuevos toman su grupo
# primario (65532, el del relay), sin chown. Cada fichero se escribe aparte y se renombra: el relay nunca
# ve un certificado a medias. smtp-relay recarga el par cuando cambia su fecha.
set -eu

src=/etc/ssl/mail
dst=/run/core-force-mail/smtp-tls
interval="${SMTP_RELAY_CERT_SYNC_INTERVAL:-300}"
case "$interval" in
  ''|*[!0-9]*) echo "sync-cert: SMTP_RELAY_CERT_SYNC_INTERVAL debe ser un numero de segundos" >&2; exit 1 ;;
esac

umask 0177
while :; do
  if [ -s "$src/cert.pem" ] && [ -s "$src/key.pem" ]; then
    if ! cmp -s "$src/cert.pem" "$dst/cert.pem" || ! cmp -s "$src/key.pem" "$dst/key.pem"; then
      cp "$src/key.pem" "$dst/.key.pem.nuevo"
      cp "$src/cert.pem" "$dst/.cert.pem.nuevo"
      # cp conserva los permisos del origen (0600): el grupo del relay se anade despues.
      chmod 0640 "$dst/.key.pem.nuevo" "$dst/.cert.pem.nuevo"
      mv -f "$dst/.key.pem.nuevo" "$dst/key.pem"
      mv -f "$dst/.cert.pem.nuevo" "$dst/cert.pem"
      echo "sync-cert: certificado del relay actualizado"
    fi
  else
    echo "sync-cert: acme aun no dejo cert.pem y key.pem en $src" >&2
  fi
  sleep "$interval"
done
