# ADR 0018: Dovecot sin replica

## Estado

Aceptado (2026-10-01). Retira de `deploy/mail/dovecot` la replica heredada de mailcow (plugin
`replication`, servicios `replicator` y `aggregator`, `mail_replica`, `repl_health.sh`) y hace que el
contenedor se niegue a arrancar si se le pasa `MAIL_REPLICA_IP` o `DOVEADM_REPLICA_PORT`.

## Contexto

mailcow trae una replica de Dovecot entre dos nodos: el plugin `replication` avisa de cada cambio, el
`replicator` pide al servidor doveadm local un `sync` del buzon, y este lo empuja por TCP (puerto 12345,
autenticado con `doveadm_password`) al `dsync-server` del otro nodo. Se activa con `MAIL_REPLICA_IP` y
`DOVEADM_REPLICA_PORT`; `doveadm_password` se escribe a mano en `extra.conf`.

Aqui la plataforma corre en un solo servidor y la recuperacion se hace con respaldos ensayados
(`docs/Operacion_Despliegue.md`, respaldos y recuperacion de desastre). Ningun documento rector plantea
alta disponibilidad con una segunda copia viva de los buzones. Aun asi, la rama de replica seguia en la
configuracion, sin probar, y el `replicator` arrancaba siempre.

Comprobado el 2026-10-01:

- En produccion, cada arranque de Dovecot dejaba un `doveadm client isn't allowed to use command: sync`
  por buzon: el `replicator`, sin destino, pedia el `sync` y la API de doveadm lo rechazaba.
- `doveadm_allowed_commands` (que restringe la API HTTP de doveadm a `kick` y `auth cache flush` para la
  revocacion de `mail-security`) es global en Dovecot 2.3.21: vale igual para el socket local del
  `replicator`, para el puerto 12345 y para la API HTTP. Probado con dos Dovecot de la imagen del proyecto:
  ni un bloque `local 0.0.0.0/0 {}` ni uno `remote 0.0.0.0/0 {}` cambian el valor por conexion.
- Por eso, con `MAIL_REPLICA_IP` puesta, la replica no funcionaba: el `sync` local se rechazaba y el
  `dsync-server` del otro nodo tambien, salvo con su permiso. Fallaba en silencio: la operacion creeria
  tener una copia que no existia.
- Hacerla funcionar exigiria anadir `sync` a esa regla global. Con el, quien tenga `DOVEADM_API_KEY`
  podria pedir por la API HTTP un `sync` de cualquier buzon hacia un servidor ajeno, que ademas recibiria
  `doveadm_password`. Esa clave la tiene `mail-security` para echar sesiones y vaciar la cache, no para
  copiar correo.
- El `replicator` desaparece en Dovecot 2.4: sostenerlo seria invertir en un mecanismo sin futuro.

## Decision

1. Dovecot no se replica. Las listas de plugins no llevan `replication`; `dovecot.conf` no declara los
   servicios `replicator` ni `aggregator` ni los ajustes `replication_*`, ni incluye `mail_replica.conf`.
2. `doveadm_allowed_commands` queda en `kick,auth cache flush`, sin `dsync-server`.
3. El entrypoint sale con error, antes de esperar a la base, si llegan `MAIL_REPLICA_IP` o
   `DOVEADM_REPLICA_PORT`, y nombra este ADR. Una replica configurada no arranca a medias.
4. `repl_health.sh` y su tarea de cron se retiran. El entrypoint sigue publicando
   `DOVECOT_REPL_HEALTH=1` al arrancar, que es lo que lee el watchdog: sin replica no hay nada que
   vigilar.
5. `make e2e-mail` comprueba que Dovecot no carga `replication`, que no hay `replicator` en marcha, que
   el registro no tiene `sync` rechazados y que el contenedor se niega a arrancar con `MAIL_REPLICA_IP`.

## Consecuencias

- Desaparece el ruido del arranque y un proceso que no hacia nada util.
- La divergencia con mailcow baja en un fichero (`repl_health.sh`), aunque `dovecot.conf` y el
  entrypoint se apartan algo mas: un cambio de mailcow en la replica no se reaplica
  (`deploy/mail/UPSTREAM.md`, secciones 5 y 8; `ops/upstream/mapa.tsv` marca `repl_health.sh` como no
  copiado).
- `netfilter` sigue leyendo `MAIL_REPLICA_IP` para su excepcion de aislamiento. Con la variable vacia, que
  es lo unico que Dovecot admite, esa excepcion no se aplica.
- Si algun dia hace falta una segunda copia viva de los buzones, se decide en un ADR nuevo, con
  `make e2e-mail` cubriendo los dos nodos. Opciones a valorar entonces: Dovecot 2.4 con su mecanismo
  propio, o `doveadm sync` lanzado desde la linea de ordenes (que no pasa por el servidor doveadm ni por
  `doveadm_allowed_commands`) contra el `dsync-server` del otro nodo, en una instancia de doveadm que no
  sea la de la API HTTP.
