# ADR 0001: `mail.relayhosts.password` y `mail.transports.password` en texto plano

## Estado

Aceptado (excepcion documentada, no resuelta).

## Contexto

`CLAUDE.md` exige que toda credencial de terceros (relayhosts, DKIM) se cifre con
`MAIL_ENCRYPTION_KEY` (AES-256-GCM). `mail.relayhosts.password` y `mail.transports.password`
(`migrations/cell/canonical/mail-directory/01_mail.sql:198,211`) no cumplen esa regla: se
guardan y se leen en texto plano.

La razon es el contrato de Postfix, no un descuido. Postfix resuelve estos mapas con
`proxy:pgsql:` (`deploy/mail/postfix/postfix.sh`): ejecuta una consulta SQL y usa el valor de
la columna tal cual, dentro del propio proceso maestro de Postfix, sin invocar a ningun
servicio Go. No hay paso intermedio donde una llamada a `mail-auth` o a cualquier otro
servicio pueda descifrar el valor antes de que Postfix lo use para autenticarse contra el
relay externo. Cifrar la columna sin mas dejaria a Postfix leyendo un blob que no sabe
interpretar, y el correo saliente por ese relay dejaria de autenticarse.

## Mitigacion ya aplicada

- **Alcance minimo por rol.** Las dos tablas tienen RLS (`migrations/cell/canonical/mail-directory/02_mail_rls.sql:42-57`)
  con dos unicos roles: `mail_app` (el propio servicio `mail-directory`, acotado a las filas
  de su tenant) y `mail_engine` (Postfix/Dovecot, solo lectura). Ningun otro servicio de la
  plataforma tiene GRANT sobre `mail.relayhosts`/`mail.transports`.
- **Nunca sale por API.** `mail-directory` no expone la columna `password` en ninguna
  respuesta HTTP (verificado en `services/mail-directory/internal/adapters/http/`).
- **Auditoria de acceso a la base** (`pkg/db` + RLS) sigue aplicando igual que al resto del
  esquema `mail`.

Con esto, el riesgo residual no es "cualquier servicio puede leer la contraseña", sino
"quien tenga acceso directo a la base de la celda (un volcado, una copia de seguridad, o el
propio host de Postgres comprometido) la ve en claro".

## Opciones consideradas

1. **Mantener texto plano, documentado (esta ADR).** Cero riesgo de romper el envio
   saliente. No reduce el riesgo de un volcado de base expuesto.
2. **Cifrar con `pgcrypto` y descifrar dentro de la misma consulta SQL del mapa** (`SELECT
   pgp_sym_decrypt(password_enc, '<clave>') FROM mail.relayhosts WHERE ...`), con la clave
   inyectada al render de `postfix.sh` igual que ya se inyecta la credencial de conexion a la
   base. Reduce el riesgo de un volcado de base robado (sin la clave, que vive solo en el
   disco del host de correo, el dato cifrado no sirve), pero no protege contra un host de
   Postfix comprometido (que ya tiene la clave) ni contra un acceso con el rol `mail_engine`.
   Requiere cambiar el generador de mapas de Postfix y probarlo contra un Postfix real antes
   de tocar produccion: un mapa mal generado detiene el envio saliente de toda la celda sin
   aviso previo.
3. **Que Postfix llame a un servicio Go por HTTP en vez de leer SQL directo.** Descarta el
   patron "motores maduros, capa propia" que ya usa `mail-auth`/`mail-policy` para todo lo
   demas; Postfix no tiene un mecanismo generico de "policy service" para el mapa de
   relayhost SASL como si lo tiene para las politticas de Rspamd. Cambio mayor, no evaluado
   en detalle.

## Decision

Por ahora, opcion 1: se documenta la excepcion en vez de tocar el generador de mapas de
Postfix sin poder validarlo contra un Postfix real en este entorno. La opcion 2 queda como
mejora futura recomendada (ver mas abajo), no como deuda silenciosa.

## Consecuencias

- El invariante de CLAUDE.md ("credenciales de terceros cifradas con `MAIL_ENCRYPTION_KEY`")
  tiene una excepcion explicita y acotada a estas dos columnas, con su razon documentada
  aqui en vez de en un comentario disperso.
- Un volcado de `mail_cell_<code>` sigue exponiendo estas contrasenas en claro a quien lo
  lea. Restringir quien puede sacar un volcado de la celda (`ops/backup`) sigue siendo el
  control real hasta que se implemente la opcion 2.

## Seguimiento

Implementar la opcion 2 (`pgcrypto` + `postfix.sh`) requiere: (a) migrar las columnas a
`bytea` cifradas manteniendo una migracion aditiva de transicion, (b) actualizar
`RelayhostRepo.Create`/`TransportRepo.Create` para cifrar al escribir, (c) actualizar
`postfix.sh` para generar el mapa con `pgp_sym_decrypt` y la clave, y (d) validar con
`make e2e` contra Postfix real antes de desplegar. No se ha hecho en esta tarea por el
riesgo de romper el envio saliente sin poder probarlo de punta a punta en este entorno.
