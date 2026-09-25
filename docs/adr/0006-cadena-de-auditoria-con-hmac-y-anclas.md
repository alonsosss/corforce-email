# ADR 0006: Cadena de hash de auditoría con HMAC, versión por fila y anclas de la cabeza

## Estado

Implementado y probado contra Postgres real (2026-09-21). Sin desplegar: el despliegue del código no cambia nada
hasta que se pone `AUDIT_HASH_KEY` en el almacén de secretos. La exportación del ancla a un sistema externo está
implementada por correo (sección 8) y queda pendiente como objeto con retención inmutable (sección "Ancla externa").

Ampliado el mismo día con dos decisiones que dejó abiertas la revisión de robustez, también implementadas y probadas
contra Postgres y NATS reales y sin desplegar: la **verificación en segundo plano** (sección 6, migración `10`) y la
**entrega de los eventos del bus por consumidores durables** (sección 7), sin la cual el rastro perdía lo publicado
mientras `audit` estaba caído.

Ampliado de nuevo el mismo día con el **ancla externa por correo** (sección 8): el informe de anclas sale del servidor
como correo de la plataforma a `AUDIT_ANCHOR_RUA`, firmado con la llave de la cadena, y `audit verificar-ancla` lo
coteja fuera. Probado contra Postgres real con un `transactional` simulado y con mutaciones del correo y de la cadena.
Sin desplegar y sin migraciones: con `AUDIT_ANCHOR_RUA` vacía nada cambia.

## Contexto

`audit.audit_logs` es el rastro de la empresa y lleva una cadena de hash desde la migración `03`: cada fila guarda el
hash de la anterior (`prev_hash`) y el suyo (`entry_hash`), y `GET /api/v1/audit/integrity` la recorre. La pasada de
pruebas contra Postgres real (`Fase0_Estado.md`, "Pruebas de `audit`") dejó cuatro debilidades que son de diseño, no
de prueba, porque cambiar el hash invalida las cadenas existentes:

1. **Ambigüedad.** El hash une los campos con `|` sin prefijo de longitud. Hay colisiones demostradas: `resource =
   "users"` y `resource_id = "u-1|x"` dan el mismo hash que `resource = "users|u-1"` y `resource_id = "x"`. Vale para
   cualquier par de campos contiguos (`action`/`module`, `module`/`resource`).
2. **`user_agent` no entra en el hash**, así que se puede editar sin que nada lo note.
3. **El hash no lleva clave.** Quien escribe en la base y conoce el formato (que está en el repositorio) recalcula la
   cadena entera tras editar lo que quiera. Y borrar las últimas filas no deja discontinuidad: lo que queda sigue
   enlazando.
4. **`audit.security_events` no está encadenada.** Un evento de seguridad se puede borrar sin rastro; el rol del
   servicio incluso tenía permiso para hacerlo.

La cadena es por base de empresa (cada empresa tiene la suya), y la clave no puede ser de una sola empresa: la lleva el
servicio, que es quien escribe.

## Decisión

### 1. Versión del hash por fila

`audit.audit_logs` gana `hash_version smallint NOT NULL DEFAULT 1` y `hash_key_id text` (`07_audit_hashchain_v2.sql`,
aditiva). Cada fila se verifica con la fórmula de **su** versión:

* **Versión 1** (todas las filas existentes, por el `DEFAULT`): la fórmula de siempre, sin tocarla y **sin reescribir
  ninguna fila**. Sus debilidades (1 y 2) se conservan en esas filas y se documentan; la prueba
  `TestLaVersion1SigueSinCubrirElUserAgent` lo deja escrito.
* **Versión 2** (filas nuevas si el servicio tiene clave): `entry_hash = hex(HMAC-SHA256(clave, canónica))` sobre una
  serialización canónica.

Serialización canónica de la versión 2 (contrato con las filas ya firmadas; la fija `TestElFormatoDeLaVersion2NoCambia`
con vectores calculados fuera de Go):

```
str(s)    = uint64 big-endian con la longitud de s || bytes de s
opt(x)    = 0x00 si NULL | 0x01 || str(x)
id(u)     = str(los 16 bytes del uuid)
tiempo(t) = int64 big-endian, microsegundos desde la época UTC
cabecera  = str("cfm-audit-chain") str(cadena) int64(2) str(key_id) int64(seq) str(prev_hash)

audit_logs      = cabecera id(id) id(tenant_id) id(user_id) opt(id)(session_id) str(action) str(module) str(resource)
                  opt(resource_id) str(ip_address) opt(user_agent) opt(request_id) opt(before) opt(after)
                  opt(changes) str(severity) tiempo(created_at)
security_events = cabecera id(id) id(tenant_id) opt(id)(user_id) str(event_type) opt(host(ip_address))
                  opt(user_agent) opt(detail) str(risk_level) tiempo(created_at)
```

Cada campo lleva su longitud, así que dos filas distintas nunca dan los mismos bytes; NULL y cadena vacía se distinguen;
`user_agent` entra; y `seq`, `prev_hash`, la cadena, la versión y el `key_id` también, de modo que una fila no se puede
mover de sitio ni pasar de una cadena a otra sin la clave. Como en la versión 1, se hashea la forma **almacenada** (el
`jsonb` como texto de Postgres, `created_at` con microsegundos), leída con `RETURNING`.

### 2. Clave, identificador y rotación

* `AUDIT_HASH_KEY`: 64 hex (32 bytes, `openssl rand -hex 32`), **independiente** de `MAIL_ENCRYPTION_KEY`. Vive solo en el
  almacén de secretos (`ops/security/secrets/secret-keys.txt`, marcada **opcional** con `?`: desplegar el código no la
  exige). La recibe solo `audit`: `docker-compose.yml` la vacía en los demás servicios y `check-secrets.sh` lo comprueba,
  como con `JWT_SIGNING_KEY`, porque el fichero de secretos llega entero a cada contenedor.
* `AUDIT_HASH_KEYS_OLD`: llaves retiradas, separadas por coma. Mismo patrón que `MAIL_ENCRYPTION_KEY` /
  `MAIL_ENCRYPTION_KEYS_OLD` (`pkg/crypto.KeyRing`), con un anillo propio para HMAC: `pkg/crypto.MACKeyRing`
  (`LoadMACKeyRing`, `ActiveID`, `SignWith`). Se rechazan retiradas sin activa, llaves repetidas y llaves mal formadas.
* **Identificador de llave**: los 8 primeros bytes (16 hex) de `HMAC-SHA256(llave, "core-force-mail/mac-key-id/v1")`. Se
  deriva de la propia llave: no es reversible, no depende de un nombre que alguien mantenga y se guarda en cada fila
  (`hash_key_id`) para saber con cuál verificarla.
* **Rotación**: se pone la llave nueva como activa y la anterior en `AUDIT_HASH_KEYS_OLD` (`rotate-key.sh` sirve para
  cargar las dos). Las filas nuevas llevan la nueva; las viejas se siguen verificando por su `key_id`. A diferencia del
  cifrado, **una llave retirada no se puede quitar** mientras existan filas firmadas con ella: una fila append-only no se
  re-firma, y sin su llave queda sin verificar (`hash_key_unknown`). Si ese costo pesa, la salida futura son puntos de
  control firmados con la llave nueva (sección "Mejoras futuras").
* **Sin clave** el servicio sigue escribiendo la versión 1 como hasta ahora y lo dice en el arranque con un aviso; **con
  clave** escribe la versión 2. La verificación de una fila de versión 2 sin clave configurada es un fallo explícito
  (`hash_key_missing`), nunca un OK.
* **La versión no retrocede**: una fila de versión 1 posterior a una de versión 2 es una rotura
  (`hash_version_regression`). Cierra el ataque de bajar las últimas filas a la versión 1 y recalcularlas sin clave.

### 3. Ancla de la cabeza

`audit.chain_anchors` (`08_chain_anchors.sql`) guarda `(tenant_id, chain, head_seq, head_hash, hash_version,
anchored_at)`, única por `(chain, head_seq)`. El rol del servicio solo puede **leer e insertar** (sin `UPDATE`,
`DELETE` ni `TRUNCATE`), igual que hace `06_append_only_role.sql` con el rastro.

Cada `AUDIT_ANCHOR_INTERVAL` (por defecto 15 minutos, de 1 minuto a 24 horas) un barrido recorre las empresas activas, bajo
un cerrojo de líder sobre el registro (`db.TryLeaderLock`, como `suppression`), y por cada cadena cuya cabeza cambió:

1. contrasta la cadena con las anclas que ya tiene: si le falta alguna **no ancla nada** y deja un error en el log
   (`audit: la cadena no contiene un ancla ya publicada`), para que un borrado no pase a ser la nueva referencia;
2. en **una sola transacción** inserta el ancla y encola por la outbox (`pkg/outbox`) el evento `audit.chain.anchored`
   (`tenant_id`, `chain`, `head_seq`, `head_hash`, `hash_version`, `anchored_at`; sin contenido de ninguna fila). El
   relé por empresa (`outbox.RunForTenants`) lo entrega al stream `AUDIT_CHAIN`; `audit` es su único dueño y hoy no lo
   consume nadie;
3. registra la cabeza en el log estructurado.

`GET /integrity` contrasta la cadena con **todas** las anclas: `head_behind_anchor` si la cabeza actual es anterior a la
más alta (se borraron las últimas filas) y `anchor_mismatch` si en la posición de alguna ancla hay otro hash o ninguna
fila (se reescribió o se borró y se siguió escribiendo). Comparar todas y no solo la última también detecta reescribir
la cadena entera en la versión 1 tras haber estado en la 2, que el recorrido de filas no ve.

### 4. `security_events`

Se encadena con el mismo esquema (`09_security_events_chain.sql`: `seq`, `prev_hash`, `entry_hash`, `hash_version`,
`hash_key_id`), con estas diferencias:

* **Solo versión 2.** No hay filas ni fórmula anteriores que conservar. Con clave, cada evento nuevo se firma y se
  encadena bajo su propio cerrojo (`pg_advisory_xact_lock`, distinto del de `audit_logs`); sin clave se escribe sin
  `seq` ni hash, como hasta ahora, y no forma parte de la cadena. Por eso `seq` **no** tiene `DEFAULT`: una fila con `seq`
  y sin hash es una rotura, no una fila sin cadena.
* **Entran los campos inmutables** (quién, qué, desde dónde, detalle, riesgo, cuándo). El reconocimiento
  (`acknowledged`, `acknowledged_by`, `acknowledged_at`) es estado de trabajo y **cambia a propósito**, así que no forma
  parte del hash: reconocer un evento no rompe la cadena (`TestReconocerUnEventoNoRompeLaCadena`).
* **Permisos**: el rol del servicio pierde `DELETE` y `TRUNCATE` y su `UPDATE` queda limitado por columna a las tres del
  reconocimiento y a los dos hashes. Es la parte del control que no depende de la clave: hasta ahora el servicio, o quien
  le robara la credencial, podía borrar eventos.
* Se ancla igual que `audit_logs` (la columna `chain` de `chain_anchors` distingue las dos).

### 5. Verificador

`GET /api/v1/audit/integrity` (permiso `audit/integrity/verify`, sin cambios) sigue devolviendo `ok`, `checked` y
`broken_id` y añade: `chain` (cuál cadena falló primero), `reason`, `broken_seq`, `broken_hash_version`, `versions` (filas
verificadas por versión de hash), `head`, `anchor` (el ancla más alta conocida) y `security_events` (el resultado de la
otra cadena con la misma forma). `ok` vale para el conjunto. Causas (`reason`):

| Código | Significa |
|---|---|
| `chain_broken` | el contenido de una fila o su enlace con la anterior no cuadran |
| `hash_key_missing` | hay filas de versión 2 y el servicio no tiene clave: no se pueden verificar |
| `hash_key_unknown` | la fila se firmó con una llave que el anillo ya no tiene |
| `hash_version_regression` | fila de versión 1 posterior a una de versión 2 |
| `hash_version_unsupported` | versión que el servicio no conoce |
| `head_behind_anchor` | la cabeza actual es anterior a un ancla ya publicada |
| `anchor_mismatch` | la posición de un ancla contiene otro hash o ninguna fila |
| `checkpoint_mismatch` | la fila donde una verificación anterior dejó su punto ya no tiene el hash que se registró, o no existe (sección 6) |

**Costo y límites de la verificación.** Recorre la cadena entera de la empresa, así que su trabajo crece con ella y lo
puede pedir cualquiera con el permiso. Se lee por paginación de clave (`seq > última`, lotes de 2000 filas), no con una
consulta larga: entre lotes se devuelve la conexión y no se retiene una instantánea que impida limpiar filas muertas, la
memoria la fija una fila y no la cadena, un contexto cancelado la corta en el siguiente lote y no bloquea a los
escritores (que solo comparten el candado entre sí). Medido con 100000 filas de versión 2 en un Postgres real: 439 ms
(227000 filas/s), +2,8 MiB de heap, cancelada a los 44 ms, y una escritura pasa de p50 0,73 ms a 0,84 ms mientras
verifica. Dentro de una petición, un recorrido por empresa y cuatro por proceso (la que sobra recibe 429
`VERIFICATION_BUSY`, no espera cola) y cada uno tiene `AUDIT_VERIFY_TIMEOUT` (25 s por defecto, tope 28: por debajo del
`WriteTimeout` de 30 s de `pkg/server`; vencido, 504 `VERIFICATION_TIMEOUT`). Una cadena de millones de filas no cabe en
ese plazo: por eso `GET /integrity` solo verifica dentro de la petición las cadenas pequeñas y las grandes pasan a la
verificación en segundo plano de la sección siguiente.

### 6. Verificación en segundo plano

**Modelo.** Cada verificación es una fila de `audit.integrity_runs` (migración `10`, por empresa): quién la lanzó
(`requested_by`), su origen (`manual`, `sweep`, `request`), su modo, su estado, la fase, las filas comprobadas, la
posición alcanzada y la de la cabeza al empezar (`current_seq`, `target_seq`: una cota del avance, no un porcentaje
exacto, porque `seq` tiene huecos), el punto de reanudación de cada cadena y el resultado. Corre en una goroutine del
proceso que la recibió, fuera de la vida de la petición, y no bloquea las escrituras (lee por lotes, como antes).

| Ruta (permiso `audit/integrity/verify`, el de siempre) | Respuesta |
|---|---|
| `POST /audit/integrity/runs` (cuerpo opcional `{"mode":"full"\|"incremental"}`, completa por defecto) | `202` con la verificación, `Location` y `Retry-After`. `409 VERIFICATION_RUNNING` (con `error.details.run_id`) si la empresa ya tiene una; `429 VERIFICATION_BUSY` si el proceso está en su tope |
| `GET /audit/integrity/runs` | las 20 más recientes de la empresa |
| `GET /audit/integrity/runs/{id}` | estado, fase, avance y, terminada, `result` (el mismo cuerpo que da `GET /integrity`) o `error` (`internal_error`, `timeout`: un fallo técnico no dice nada de la cadena) |
| `POST /audit/integrity/runs/{id}/cancel` | la verificación con `cancel_requested`; el dueño la cierra como `cancelled` en su siguiente punto. Es POST y no `DELETE` porque el gateway exige la acción `delete` para `DELETE` y la verificación solo pide `integrity/verify` |
| `GET /audit/integrity` | si la suma de las posiciones de cabeza de las dos cadenas no pasa de `AUDIT_INTEGRITY_INLINE_MAX_ROWS` (200000 por defecto), **igual que antes**: `200` con el veredicto. Si pasa, `202` con `{"run": ..., "last_completed": ...}`: lanza una verificación completa (origen `request`) o sigue la que ya corre, y da la última terminada para que el llamador tenga algo que mirar |

Decisión sobre `GET /integrity`: conservar el contrato para lo que ya cabía, y para lo que no, contestar `202` en vez de
agotar el plazo. Un `GET` con efecto lateral es poco limpio, pero el `GET` ya era la operación costosa (la única forma
de pedir el veredicto) y sigue exigiendo `integrity/verify`; el efecto está acotado (una verificación por empresa a la
vez, con el mismo cupo) y el llamador de un cliente nuevo usa el `POST`, que es lo que hace la web.

**Un trabajo por empresa, con cerrojo en la base.** El índice único parcial `uq_integrity_runs_active` (una fila
`running` por `tenant_id`) es el cerrojo: vale entre procesos y entre réplicas, y dos lanzamientos a la vez dejan pasar
a uno (probado con tres procesos y doce lanzamientos concurrentes). El tope global es **por proceso** (4, el mismo cupo
que las verificaciones dentro de la petición): con N réplicas son 4 por réplica.

**Reanudable.** El dueño anota su avance (fase, punto de cada cadena y `heartbeat_at`) como mucho cada 2 s. Si el proceso
muere, `heartbeat_at` deja de avanzar; pasados 90 s la verificación se considera abandonada y la retoma quien la mire
(`GET /runs/{id}`), quien lance otra (`POST`), o el barrido, **desde su punto y no desde cero** (probado matando el
proceso a mitad y retomando con otro). El latido se compara con el reloj de la base, no con el de los procesos. Un
apagado ordenado deja la verificación en curso con su punto, igual que una muerte. El plazo total de una verificación es
`AUDIT_INTEGRITY_RUN_TIMEOUT` (12 h por defecto): vencido, queda `failed` con `timeout`.

**El punto de reanudación está atado a la fila, no a un número.** Cada cadena guarda `(seq, hash, hash_version,
saw_keyed, filas y versiones contadas)` de la última fila verificada. Al retomar, antes de seguir, se **relee la fila de
posición `seq`**, se comprueba que su contenido da su propio hash (con el `prev_hash` que tiene guardado: de ahí sale el
enlace con la anterior) y que ese hash es el registrado (comparación en tiempo constante); solo entonces se exige que la
fila siguiente enlace con él. Así, editar el contenido de la fila del punto o su hash da `chain_broken` en esa
posición, y borrarla da `checkpoint_mismatch`; borrar filas justo después rompe el enlace de la siguiente. Probado con
manipulaciones entre la muerte del proceso y su reanudación; quitar esa relectura hace fallar tres de esas pruebas.

Lo que **no** cubre, y por eso existe la verificación completa: una verificación que parte de un punto (reanudada o
incremental) confía en lo que ya verificó. Una fila **anterior** al punto que se edite después no la ve; la ve la
verificación completa (probado: la incremental da OK y la completa da la fila afectada). Las anclas sí se contrastan
enteras en cada verificación. El punto guardado está en la misma base que la cadena y no va firmado: contra el rol del
servicio, que no puede escribir filas del rastro, no hace falta; contra quien escribe como dueño de la base, que también
puede escribir un resultado falso en `integrity_runs`, no basta, y la respuesta es el ancla externa.

**Incremental.** Parte del punto final de la **última verificación terminada**, y solo si esa dio la cadena por buena;
sin ella (o si la última la dio por rota, aunque una anterior fuera buena) es completa. Partir de una buena anterior a
una rotura haría parecer sana una cadena que ya se sabe rota. `checked` cuenta las filas de la cadena verificadas en
total (las del punto más las nuevas), no las releídas.

**Barrido periódico (`AUDIT_INTEGRITY_SWEEP_INTERVAL`, apagado por defecto).** De 1 h a 7 d. Bajo un cerrojo de líder
sobre el registro (`db.TryLeaderLock`), una empresa a la vez y con hasta 30 min por empresa y pasada (lo que no termine
queda en curso con su punto y la siguiente pasada lo retoma), retoma lo abandonado o lanza una verificación incremental,
y completa si la última completa correcta pasó de `AUDIT_INTEGRITY_FULL_EVERY` (7 d). La primera pasada espera 5 minutos
tras arrancar. Una empresa con la cadena rota se verifica completa en cada pasada hasta que se resuelva (la incremental
no parte de una rotura): es el costo de que la alerta siga en pie, y no hay todavía forma de dar una rotura por
conocida (sección "Mejoras futuras").

**Métricas y alertas.** `audit_integrity_runs_total{origin,outcome}` (`ok`, `broken`, `cancelled`, `failed`),
`audit_integrity_sweep_broken_tenants` (empresas rotas en la última pasada completa) y `audit_events_discarded_total`.
Alertas en `ops/observability/prometheus/rules/plataforma.yml`, grupo `auditoria`, con pruebas de `promtool`:
`CadenaDeAuditoriaRota` (crítica: el gauge o una verificación rota en la última hora), `VerificacionDeCadenaSinTerminar`
(media: el barrido falla por una causa técnica más de 30 minutos) y `EventosDeAuditoriaDescartados` (media). Ninguna
etiqueta lleva la empresa: la empresa, la cadena, el motivo y la posición están en el registro del servicio y en la fila
de `integrity_runs`.

**Permisos de la tabla.** `audit_service` inserta, lee y actualiza solo las columnas de estado de una verificación en
curso; nunca borra, y no puede editar quién la lanzó, cuándo ni con qué modo (probado con `permission denied`). La fila es
el registro de quién verificó y con qué resultado.

### 7. Los eventos del bus llegan por consumidores durables

Hasta ahora `audit` recibía los eventos de dominio por una suscripción de **núcleo** (`QueueSubscribe`, sin estado): todo
lo publicado con `audit` caído (cada despliegue, cada reinicio) se perdía del rastro, y un servicio cuyo único fin es ser
evidencia no puede perder evidencia por reiniciarse. Ahora hay **un consumidor durable de JetStream por subject** de
`AUDIT_SUBJECTS` (`identity.>`, `organization.>`, `access.>`, `gateway.>`, `scheduler.>`, `domains.>`, `migration.>`,
`webmail.assistant.used` y los cuatro hechos de seguridad de los buzones de `mail-directory`, `mail.mailbox.mfa_enabled`,
`mail.mailbox.mfa_disabled`, `mail.mailbox.forwarding_changed` y `mail.policy.updated`, por defecto), con nombre `audit-<subject>` (`audit-identity-all`, ...):

* **Stream.** El del dueño del subject (`IDENTITY`, `ORGANIZATION`, `SCHEDULER`, `DOMAINS`, `MIGRATION`,
  `MAIL_DIRECTORY` para `mail.>`) o, si no hay (`gateway.>`, `access.>`), uno propio con el primer token en mayúsculas
  (`GATEWAY`, `ACCESS`). `audit` lo declara con `EnsureStream`, que une subjects y nunca quita los ajenos (un subject
  que el comodin del dueño ya captura, como `mail.policy.updated` con `mail.>`, no se añade: JetStream rechaza subjects
  solapados). Un `Publish` de
  núcleo a un subject cubierto por un stream también se retiene, así que `identity`, que publica sin JetStream, queda
  cubierto sin tocarla. Un subject nuevo en `AUDIT_SUBJECTS` necesita que su primer token dé el nombre del stream
  del dueño; si no, `EnsureStream` falla por subjects solapados y `audit` lo registra y reintenta.
* **Idempotencia.** El id del apunte es el del evento (o, si no es un uuid, uno derivado de su texto): una reentrega choca
  con el apunte ya guardado en lugar de duplicarlo. El detector de seguridad corre solo para un apunte **nuevo**, así una
  reentrega no repite las alertas.
* **Ack solo tras persistir.** Guardar y luego confirmar; el detector va después de confirmar, así un fallo suyo no
  reentrega. Sin confirmar quedan los fallos que una nueva entrega puede arreglar (la base de la empresa no responde):
  JetStream reentrega a los 90 s hasta 20 veces (30 minutos) y después el mensaje va a `EVENTS_DLQ`, que alerta
  (`EventosAbandonadosEnDLQ`). No hay un retroceso creciente entre reentregas: el intervalo es el `AckWait` fijo de
  `pkg/events`, y cambiarlo tocaría a todos los consumidores. Un cuerpo que no es un evento va a la DLQ sin reintentos, y un
  `panic` del manejador lo recupera `pkg/events`. Los mensajes van de una en una por subject: uno colgado retiene a los de
  su subject como mucho 30 s (el plazo del manejador) y los demás subjects no esperan; uno que falla no bloquea a los que
  vienen detrás (probado contra NATS real).
* **Lo que se da por tratado sin apunte**, contado en `audit_events_discarded_total` y registrado: un evento sin
  `tenant_id` (no hay base donde escribirlo) y uno de una empresa que ya no existe en el registro (reintentar no la haría
  aparecer). Antes se descartaban sin dejar cuenta.
* **NATS caído no impide arrancar.** `events.NewBus` reconecta solo y los consumidores (los de dominio y el del rastro del
  API, que hasta ahora no reintentaba nunca) reintentan atarse cada 5 s; el servicio atiende su API mientras tanto.
* **Transición.** El durable se **crea** con `DeliverNew`: solo recibe lo publicado desde ese momento. Con `DeliverAll`
  reproduciría los 7 días que los streams ya retienen (`SCHEDULER`, `DOMAINS`, `MIGRATION`, `ORGANIZATION`,
  `IDENTITY` para `user.deleted`), y la suscripción anterior ya guardó esos eventos con un id **distinto** (los ponía
  al azar): no habría idempotencia que los reconociera y quedarían duplicados en un rastro que es evidencia. El
  costo es acotado y conocido: lo publicado entre que el `audit` viejo se detiene y el nuevo crea sus consumidores (los
  segundos de un despliegue) se pierde **una sola vez**, como se perdía en cada despliegue hasta hoy; desde el primer
  arranque nuevo, no más. Un durable que ya existe conserva su posición y esta política no le afecta. Para no duplicar,
  el `audit` viejo no debe convivir con el nuevo: se recrea el servicio, no se escala con las dos versiones a la vez.
* **Réplicas.** El consumidor es un push durable sin grupo de reparto (como los de `mail-dav` y `mail-migration`): la
  segunda réplica no puede atarse y reintenta con aviso hasta que la primera se vaya, y entonces toma el relevo. Los
  eventos se aplican una vez; lo que se repartía entre réplicas con el `QueueSubscribe` viejo ya no se reparte.
* **Retención.** Los streams retienen 7 días (y 1 GiB): un `audit` caído más de 7 días pierde lo más antiguo. Los eventos
  de identidad (con `ip` y `user_agent`) se guardan ahora 7 días en el disco de NATS además de pasar por él.

Probado contra un NATS 2.10 con JetStream y un Postgres reales (`make test-integration`, `services/audit/internal/adapters/nats`):
con el consumidor parado se publican 60 eventos (mitad por `Publish` de núcleo, mitad por JetStream, uno repetido), se
arranca y quedan 60 apuntes exactamente una vez, con la cadena de hash intacta y el detector viendo 60 apuntes nuevos;
el durable nuevo no reproduce lo que el stream ya retenía; un cuerpo ilegible va a `EVENTS_DLQ` con sus cabeceras y los
tres eventos siguientes se guardan; y un evento que no se guarda queda pendiente de confirmar sin retener a los cuatro
siguientes. Confirmar antes de guardar hace fallar la del evento pendiente y tres pruebas unitarias del consumidor.

`hash_key_missing` y `hash_key_unknown` son problemas de configuración hasta que se demuestre lo contrario; el resto son
señal de manipulación. La respuesta **nunca** incluye la llave ni su identificador, y **no nombra filas de otra empresa**:
si la fila que rompe la cadena tiene otro `tenant_id` (una base con filas mezcladas, o un `tenant_id` editado) el
veredicto sigue en rojo pero sin `broken_id` ni `broken_seq`. La comparación de hashes es en tiempo constante.

### 8. Ancla externa por correo

**Qué resuelve.** El ancla de la sección 3 vive en la misma base que la cadena: contra quien escribe como dueño de la
base (borra las últimas filas **y** las últimas anclas, o reescribe una fila y recalcula desde ahí con la llave) no
protege, y el stream `AUDIT_CHAIN` y el log están en el mismo servidor. Hacía falta una copia de la cabeza en un sitio
que ese atacante no controle. La decisión de este ADR era un correo a una dirección externa o un objeto con retención
inmutable fuera del host; lo que faltaba era el destino y sus credenciales. **El correo no exige ninguna credencial
nueva**: `transactional` ya envía los correos de la propia plataforma por SES con `POST /internal/send-email` (el
contrato que usa `identity` para el reinicio de contraseña: remitente `PLATFORM_FROM_EMAIL`, empresa
`PLATFORM_TENANT_ID`, sin pasar por reputación), así que esa es la vía que se implementa. El objeto inmutable sigue
pendiente (sección "Ancla externa").

**Barrido periódico, no consumidor del evento.** `audit.chain.anchored` existe si y solo si existe el ancla, y un
consumidor durable de ese evento habría sido lo más literal. Pero el evento sale cada `AUDIT_ANCHOR_INTERVAL` (15
minutos) por cadena y empresa, y un correo por evento son cientos al día que nadie archiva ni coteja; lo que quien
recibe necesita es **un resumen** con la última ancla de cada cadena de cada empresa. Por eso el informe lo hace un
barrido (`sweep.AnchorReportRunner`) bajo cerrojo de líder sobre el registro: lee `audit.chain_anchors` de cada empresa
activa (`Findings.Last` por cadena, con el slug de `organization.v_tenant_routing`, la única vista del registro que el
rol de `audit` lee) y lo envía en **un correo** por dirección. La consecuencia es que el informe lleva lo que la tabla
dice en ese momento: si alguien borró anclas antes del informe, el correo no las tiene. Lo que sí tiene siempre es la
última cabeza que el servicio vio, y el informe anterior tiene la de un intervalo antes; es la misma garantía que el
evento, con la frecuencia de un archivo de correo.

**Calendario alineado al reloj.** Sale en cada múltiplo de `AUDIT_ANCHOR_REPORT_INTERVAL` (24 h por defecto, de 1 h a
7 d) desde la época UTC: con 24 h, a las 00:00 UTC. Alinear al reloj y no al arranque hace que un despliegue ni repita
el informe ni lo salte, sin guardar en ninguna parte cuándo salió el último, y que el asunto de cada día sea predecible
(`[Core Force Mail] Anclas de auditoria 2026-09-22`) para quien lo archiva o lo filtra. Con dos réplicas, las dos
despiertan en el mismo instante y el cerrojo decide cuál envía; si la primera termina antes de que la segunda pruebe,
salen dos correos iguales, que no son un problema.

**Envío inmediato por rotura.** Además, cuando una verificación (en línea o en segundo plano) o el propio anclaje dan
una cadena por rota, `audit` envía en el acto el informe de esa empresa con la rotura (`cause: chain_broken`, y la
línea `broken:` con empresa, cadena y motivo) y sus anclas, para que la evidencia salga del servidor antes de que
nadie la borre. Sale fuera del hilo de la petición o del barrido que la detectó, con un contexto propio de 2 minutos,
y como mucho **una vez por hora y empresa**: una cadena rota se re-verifica en cada pasada del barrido y en cada `GET
/integrity`, y un correo por cada una no añade evidencia. El puerto es `ChainBreakNotifier`; sin `AUDIT_ANCHOR_RUA` el
caso de uso no tiene a quién avisar y no cambia nada.

**Formato del correo.** Texto plano (`text_body`, sin HTML): un archivo de correo lo conserva legible y un cliente no
lo reescribe. Lleva **solo** identificadores de empresa (id y slug), la cadena, la posición y el hash de la cabeza, la
versión del hash y el instante del ancla; nunca contenido de apuntes, IP, `user_agent` ni usuario (la prueba
`TestElCuerpoSoloLlevaIdentificadoresPosicionesYHashes` fija el contrato de cada línea). El bloque entre
`-----BEGIN CORE FORCE MAIL AUDIT ANCHORS-----` y `-----END ...-----` es lo que se firma: `format`, `generated_at`,
`cause`, `broken:` si la hay, y una línea `anchor: tenant=... slug=... chain=... seq=... hash=... hash_version=...
anchored_at=...` por cadena, con las empresas por id y las cadenas por nombre para que el mismo contenido dé los mismos
bytes. Después, `signature: hmac-sha256 key_id=<id> mac=<hex>`: HMAC-SHA256 del bloque con la **llave activa de la
cadena** (`AUDIT_HASH_KEY`, el mismo anillo y el mismo `key_id` de la sección 2), de modo que quien recibe el correo
puede comprobar que no se fabricó ni se alteró, y un informe firmado con una llave ya retirada se comprueba por su
`key_id` con `AUDIT_HASH_KEYS_OLD`. Sin llave el informe sale con `signature: none` y lo dice en el cuerpo; el
verificador que sí tiene llave lo trata como no fiable, porque un correo fabricado diría exactamente eso.

**Verificador externo.** `ops/security/verificar-ancla.sh <correo.eml | texto> [--dsn DSN [--tenant id|slug]]`
ejecuta `go run ./services/audit verificar-ancla`: un subcomando del propio binario, así que el formato del informe y
el anillo de llaves son los del código que lo envió y no una copia en bash. Lee un `.eml` completo (baja por las partes
MIME hasta el `text/plain` y decodifica quoted-printable o base64, que es como SES lo entrega) o el texto pegado,
comprueba la firma con `AUDIT_HASH_KEY`/`AUDIT_HASH_KEYS_OLD` del entorno y, con `--dsn`, coteja cada ancla con la
cadena actual por la base de la empresa (`ChainAnchorRepo.Facts`: posición de la cabeza, hash que ocupa hoy la posición
del ancla y si `chain_anchors` la conserva) con los mismos códigos que el verificador del servicio: `head_behind_anchor`
si la cabeza actual va por detrás del ancla del correo (se borraron las últimas filas) y `anchor_mismatch` si en esa
posición hay otro hash o ninguna fila (se reescribió, o se borró y se siguió escribiendo). Se eligió la base y no `GET
/integrity` porque el API da la cabeza y el ancla más alta que **el servidor** conoce, y un atacante que borró anclas
deja el API en verde; la comparación que vale es "la fila que hoy ocupa la posición del correo tiene el hash del
correo". Salida: 0 auténtico y contenido en la cadena, 1 evidencia de manipulación, 2 no se pudo comprobar. La firma
inválida manda sobre un cotejo correcto.

**Métricas y alerta.** `audit_anchor_reports_total{result}` (`sent`, `suppressed`, `rejected`, `failed`, una serie por
envío y dirección), `audit_anchor_report_last_success_timestamp_seconds` (último informe que salió hacia al menos una
dirección) y `audit_anchor_report_interval_seconds` (el intervalo configurado, 0 desactivado). `AnclaDeAuditoriaSinEnviar`
(alta) avisa cuando pasan más de dos intervalos sin envío correcto con el informe activado, con la misma forma que
`RepasoDKIMDetenido` (una réplica sin envíos cuenta desde su arranque; entre réplicas vale la última que lo logró) y el
intervalo leído de la métrica, así que vale para cualquier `AUDIT_ANCHOR_REPORT_INTERVAL`.

**Probado** (`make test-integration`, `services/audit/internal/adapters/postgres/anchor_report_integration_test.go`,
contra Postgres real y un `transactional` simulado con `httptest` que captura el correo): el informe sale como la
empresa de plataforma con el token interno, en texto plano y sin datos de los apuntes; el verificador lo lee tal cual
y da OK por el mismo DSN, también después de que la cadena crezca; alterar una posición del correo o quitar la firma es
evidencia aunque la cadena esté intacta; borrar las últimas filas **y** sus anclas deja la cadena enlazada y el API en
verde, y el correo lo delata (`head_behind_anchor`, con aviso de que `chain_anchors` ya no conserva el ancla); reescribir
la fila anclada y recalcular con la llave se delata (`anchor_mismatch`); y una verificación rota envía el aviso
inmediato una sola vez por hora. Las pruebas unitarias cubren el formato, el parseo estricto (bloque repetido, línea
desconocida, campo de más, mac corto), el `.eml` con quoted-printable, la llave retirada, el freno del aviso y los
tres enganches del caso de uso.

## Alternativas

1. **Corregir solo la ambigüedad (longitud por campo) sin clave.** Descartada: no impide recalcular la cadena, que es la
   debilidad que más importa.
2. **Firma asimétrica (Ed25519) por fila.** Permitiría verificar fuera del servidor sin conocer un secreto, pero el
   servicio tiene que guardar la clave privada, así que para el atacante que ya está en el servicio no cambia nada, y
   cada fila crece 64 bytes. Se deja como mejora si la verificación externa lo pide.
3. **Reescribir las filas antiguas a la versión 2.** Descartada: el rastro es de solo añadir (`06`), reescribirlo con la
   credencial de servicio es exactamente lo que se quiere impedir, y hacerlo con la del dueño destruiría la evidencia
   que aún vale.
4. **Triggers que rechacen `UPDATE`/`DELETE` sobre `audit_logs`.** Complementaria, no sustituta: el dueño de la tabla los
   quita. Ya se aplica el mismo principio con permisos por columna, que sí vinculan al rol del servicio.
5. **Árbol de Merkle con puntos de control firmados** en vez de una cadena. Más escalable para verificar un rango, pero
   otro formato entero de almacenamiento y verificación; sin métricas que lo pidan (`CLAUDE.md`, "No se introduce
   infraestructura nueva sin ADR").
6. **Guardar el ancla solo fuera de la base.** Es la meta (sección "Ancla externa"): la copia por correo está hecha
   (sección 8) y la de objeto inmutable necesita un destino externo y credenciales que hoy no existen.
7. **Un consumidor durable de `audit.chain.anchored` que envíe un correo por evento**, en vez del barrido de la sección
   8. Descartado: cientos de correos al día por empresa y cadena que nadie archiva; el resumen periódico lleva la misma
   cabeza con la frecuencia de un archivo de correo, y la rotura tiene su envío inmediato.
8. **Cotejar el correo con `GET /integrity` en vez de con la base.** Descartado: el API da la cabeza y el ancla más alta
   que el servidor conoce, y un atacante que borró anclas y filas lo deja en verde; la comparación que vale es la fila
   que hoy ocupa la posición del correo (sección 8).

## Ancla externa (correo implementado; objeto inmutable pendiente)

**Lo que protege el ancla dentro de la base.** La tabla, el evento y el log detectan el borrado de las últimas filas y
la reescritura de la cadena contra un atacante que **no pueda escribir también `audit.chain_anchors`** (el rol del
servicio no puede; el dueño de la base sí). Un ancla dentro de la **misma base** no protege contra quien escribe en
ambas tablas: borra las últimas filas y las últimas anclas.

**Lo que la completa.** Sacar la cabeza a un sistema que ese atacante no controle. De los dos destinos decididos:

1. **Correo a una dirección externa: hecho** (sección 8). Un resumen por intervalo con la última ancla de cada cadena
   de cada empresa, firmado con la llave de la cadena, a `AUDIT_ANCHOR_RUA` por el correo de la plataforma que ya
   existe; y el informe inmediato de la empresa cuya cadena una verificación da por rota. Un correo enviado no se puede
   borrar desde el servidor y no necesitó infraestructura ni credenciales nuevas. La dirección debe ser **externa a la
   plataforma** (un buzón en otro proveedor) y quien la recibe debe **conservar los correos fuera del servidor**: son
   la referencia con la que `ops/security/verificar-ancla.sh` coteja la cadena si el servidor se ve comprometido.
2. **Un objeto con retención inmutable (S3 con Object Lock o equivalente) fuera del host: pendiente.** MinIO en el mismo
   host no sirve (el atacante que escribe en la base escribe ahí) y sigue sin haber un destino con credenciales
   decidido. Cuando lo haya, el mismo informe (el bloque firmado) es lo que se guarda.

Lo que el correo no cubre: un atacante que controle también la salida por SES o la cuenta de correo receptora, y el
tramo entre el último informe y el ataque (como mucho un intervalo, salvo que una verificación lo vea antes y dispare
el envío inmediato).

## Migración y despliegue

Orden, cada paso reversible salvo el último:

1. **Migraciones `07`, `08` y `09` en cada base de empresa, antes del código** (`bash ops/apply-all-canonical.sh`; son
   idempotentes y aditivas). Tras el `ALTER TABLE`, `ops/maintenance/pgbouncer-reconnect.sh`. Sin ellas el servicio nuevo
   falla al insertar: sus columnas son obligatorias, igual que las de la cadena original (`03`).
2. **Desplegar el código sin clave.** Nada cambia en lo que se escribe: filas en versión 1, eventos sin cadena, y ya se
   ancla la cabeza. El arranque avisa: `audit: sin AUDIT_HASH_KEY`. La `09` retira a `audit_service` los permisos de
   borrar y reescribir eventos, que el código nunca usó.
3. **Generar la llave** (`openssl rand -hex 32`), guardarla con `ops/security/secrets/add-secret.sh AUDIT_HASH_KEY` y
   recrear **solo `audit`** con `with-secrets.sh` (las variables se fijan al crear el contenedor). El arranque registra el
   identificador de la llave. Desde ahí las filas son de versión 2 y los eventos se encadenan. Con varias réplicas de
   `audit`, ponerla en todas a la vez: una réplica sin clave escribiría filas de versión 1 tras las de versión 2 y el
   verificador las daría por regresión.
4. **Comprobar** `GET /api/v1/audit/integrity`: `versions` debe mostrar `{"1": n, "2": m}` y `ok: true`.
5. **Guardar una copia de la llave con el resto del respaldo de secretos.** Perderla deja las filas de versión 2 sin
   verificar (`hash_key_missing`). Este paso no es reversible: una vez puesta, quitarla convierte toda fila posterior en
   rotura, por diseño.

Ampliación (verificación en segundo plano y consumidores durables), con este orden:

1. **Migración `10` (`audit.integrity_runs`) en cada base de empresa, antes del código** (`bash
   ops/apply-all-canonical.sh`; idempotente y aditiva) y `ops/maintenance/pgbouncer-reconnect.sh`. Sin ella el servicio
   nuevo arranca y atiende, pero `POST /integrity/runs` y el `GET /integrity` de una cadena grande fallan con 500
   (`relation does not exist`); las cadenas pequeñas y todo lo demás siguen igual.
2. **Recrear `audit` (no escalarlo con las dos versiones a la vez).** En el primer arranque crea, con `DeliverNew`, un
   consumidor durable por subject de `AUDIT_SUBJECTS` y declara sus streams (`GATEWAY` y `ACCESS` nuevos; `IDENTITY`,
   `ORGANIZATION`, `SCHEDULER`, `DOMAINS` y `MIGRATION` ganan sus subjects): desde ese momento lo publicado con `audit` caído
   se conserva. Lo que ocurre en el hueco entre el `audit` viejo y el nuevo se pierde esa única vez. El arranque registra
   `audit: suscrito a los eventos` por cada subject.
3. **Nada más cambia por defecto**: el barrido periódico está apagado (`AUDIT_INTEGRITY_SWEEP_INTERVAL` vacío). Para
   encenderlo, ponerlo en el `.env` (por ejemplo `6h`), recrear `audit` y vigilar `audit_integrity_runs_total`.
4. **Comprobar**: `POST /api/v1/audit/integrity/runs` y `GET .../runs/{id}` hasta `completed`; `nats consumer ls IDENTITY`
   debe listar `audit-identity-all` y `nats consumer ls EVENTS_DLQ` no debe crecer.

Ampliación (ancla externa por correo), sin migraciones:

1. Elegir una dirección **fuera de la plataforma** que archive lo que recibe, y ponerla en `AUDIT_ANCHOR_RUA` del `.env`
   (varias, separadas por coma). Con ella puesta, `audit` exige `TRANSACTIONAL_URL` y `PLATFORM_TENANT_ID` (ya en el
   `.env` para `identity`) y no arranca sin ellas: un informe configurado que no puede salir es un error, no un aviso.
   `AUDIT_ANCHOR_REPORT_INTERVAL` (24h por defecto) fija el ritmo.
2. Recrear `audit`. El arranque registra `audit: proximo informe de anclas` con el instante del siguiente envío; si no
   hay llave, avisa de que el informe saldrá sin firma.
3. Comprobar que el primer correo llega y guardarlo fuera; `ops/security/verificar-ancla.sh <correo>` con la llave del
   respaldo de secretos debe dar `Firma correcta`. Vigilar `audit_anchor_reports_total{result}` y
   `AnclaDeAuditoriaSinEnviar`.

## Riesgos

* **La llave es el punto único.** Quien tenga `AUDIT_HASH_KEY` y escriba en la base recalcula las filas de versión 2.
  Mitigación: solo la recibe `audit` (compose y `check-secrets.sh`), vive en el almacén y no se imprime nunca; el ancla
  externa la deja sin valor contra el borrado de la cola.
* **El tramo en versión 1 conserva sus debilidades.** Las filas anteriores a la llave siguen siendo ambiguas, sin
  `user_agent` y recalculables. Las anclas registradas desde el primer despliegue son lo único que las protege.
* **Bajar toda la cadena a versión 1** no lo ve el recorrido de filas (queda consistente); lo ve el ancla si se publicó
  antes del ataque. Está probado en las dos direcciones (`TestBajarTodaLaCadenaAV1SoloLoDetectaElAncla`).
* **Una fila sin `seq` ni hash no forma parte de la cadena.** Insertar así una fila falsa no la rompe (ya ocurría con la
  versión 1): quien lea el rastro por SQL sin pasar por el servicio no debe fiarse de una fila sin hash.
* **`chain_anchors` crece sin poda** (una fila por cambio de cabeza y intervalo, como mucho 96 al día por cadena y empresa)
  y el verificador las contrasta todas con un join por índice. Es lineal y barato hoy; a años vista hace falta compactarlas
  (mantener una por día más las de los extremos).
* **Quitar la clave tras ponerla** deja el verificador en rojo por diseño (`hash_key_missing` y
  `hash_version_regression`): es lo mismo que vería un atacante que intente volver a la versión 1.
* **Verificar es lineal** en el tamaño de la cadena; la versión 2 añade un HMAC por fila, sin coste apreciable frente
  a la lectura. El recorrido lee las filas en flujo (memoria acotada a una fila), pero cada uno ocupa una conexión y lee la
  tabla entera de una empresa en una base que comparten todas, así que `audit` lo limita: **uno por empresa** (en la base,
  entre réplicas) y **cuatro a la vez por proceso** (`verificationGate` en `app`; con N réplicas son 4 por réplica, no 4
  en total), 429 `VERIFICATION_BUSY` al resto. Las cadenas grandes ya no se verifican dentro de la petición (sección 6).
  **Sin medir aún a escala**: las pruebas recorren miles de filas; la velocidad de la sección 5 (100000 filas) es la
  única medida. Medir en el servidor con una empresa grande antes de fijar `AUDIT_INTEGRITY_INLINE_MAX_ROWS` y el
  intervalo del barrido.
* **Una verificación incremental o reanudada confía en lo ya verificado**: una fila anterior a su punto editada después
  solo la ve la completa (sección 6). Por eso el barrido repite la completa cada `AUDIT_INTEGRITY_FULL_EVERY`, y las
  anclas se contrastan enteras en toda verificación.
* **Una cadena rota se re-verifica entera en cada pasada del barrido** (la incremental no parte de una rotura), con hasta
  30 minutos por empresa y pasada, mientras nadie la dé por conocida.
* **`GET /integrity` con efecto lateral** en las cadenas grandes (lanza una verificación completa): acotado por el cupo
  de una por empresa, y `integrity/verify` sigue siendo el único permiso.
* **Los eventos del bus**: el `audit` viejo pierde una sola vez lo publicado entre su parada y la creación de los
  consumidores nuevos (sección 7, "Transición"); un `audit` caído más de 7 días pierde lo más antiguo (retención de los
  streams); tras 20 entregas fallidas (30 minutos) un evento pasa a `EVENTS_DLQ` y hay que reproducirlo a mano.
* **Rotación sin retirada**: cada llave retirada debe conservarse mientras existan filas con su `key_id`. Los informes
  de anclas firmados con una llave retirada se comprueban con ella por su `key_id`: la llave retirada hace falta también
  para leer los correos antiguos.
* **El informe de anclas depende de SES y del receptor.** Si `transactional` o SES no responden, el envío falla y no se
  reintenta hasta el siguiente intervalo (o la siguiente verificación rota); `audit_anchor_reports_total{result="failed"}`
  lo cuenta y `AnclaDeAuditoriaSinEnviar` avisa tras dos intervalos. Si la dirección rebota o se queja, `suppression` la
  suprime y el informe deja de salir (`result="suppressed"`) hasta que se retire la supresión o se cambie la dirección.
  Si el receptor pierde los correos, la referencia externa desaparece con ellos: el correo protege solo lo que se
  conserva fuera. Y el informe lleva lo que `chain_anchors` dice en ese instante: un borrado de anclas anterior al
  informe no está en él, aunque sí en el anterior.
* **El aviso inmediato de rotura sale como mucho una vez por hora y empresa**, también si el envío falló: una rotura
  detectada durante una caída de SES espera al informe periódico o a la siguiente verificación pasada esa hora.
* **Un informe sin firma no prueba nada**: sin `AUDIT_HASH_KEY` el correo sale con `signature: none` y el verificador
  con llave lo rechaza. Poner la llave antes que la dirección.

## Mejoras futuras (no implementadas)

* Copia del informe firmado en un objeto con retención inmutable fuera del host (sección "Ancla externa"), cuando exista
  un destino con credenciales.
* Puntos de control firmados con la llave activa cada cierto número de filas, para poder retirar llaves viejas.
* Métrica de Prometheus por ancla ausente en el propio anclaje, además del correo inmediato y del log (la verificación
  ya avisa de `head_behind_anchor` y `anchor_mismatch` por `CadenaDeAuditoriaRota`, pero el barrido no la da por rota
  hasta que corre).
* Compactación de anclas antiguas.
* Cupo global de verificaciones entre réplicas (hoy por proceso; el de una por empresa ya vale entre réplicas), por
  ejemplo en Redis.
* Dar una rotura por conocida (con quién y por qué) para que el barrido no repita la verificación completa de esa empresa
  en cada pasada.
* Un consumidor durable en grupo de reparto para `audit` si el volumen de eventos pidiera repartir la aplicación entre
  réplicas, y retroceso creciente entre reentregas (hoy fijo por `pkg/events`).
* Marcar el origen de cada apunte (`POST /logs` y `/logs/bulk` los escribe quien tenga `audit/logs/create`, con
  cualquier módulo y acción, a su propio nombre): hoy un apunte del API es indistinguible de uno del gateway o del bus.
