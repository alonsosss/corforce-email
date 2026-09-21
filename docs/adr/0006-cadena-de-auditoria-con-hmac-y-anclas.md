# ADR 0006: Cadena de hash de auditoría con HMAC, versión por fila y anclas de la cabeza

## Estado

Implementado y probado contra Postgres real (2026-09-21). Sin desplegar: el despliegue del código no cambia nada
hasta que se pone `AUDIT_HASH_KEY` en el almacén de secretos. La exportación del ancla a un sistema externo está
decidida y pendiente (sección "Ancla externa").

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

**Costo y límites de la verificación.** Recorre la cadena entera de la empresa, así que su trabajo crece con ella y lo
puede pedir cualquiera con el permiso. Se lee por paginación de clave (`seq > última`, lotes de 2000 filas), no con una
consulta larga: entre lotes se devuelve la conexión y no se retiene una instantánea que impida limpiar filas muertas, la
memoria la fija una fila y no la cadena, un contexto cancelado la corta en el siguiente lote y no bloquea a los
escritores (que solo comparten el candado entre sí). Medido con 100000 filas de versión 2 en un Postgres real: 439 ms
(227000 filas/s), +2,8 MiB de heap, cancelada a los 44 ms, y una escritura pasa de p50 0,73 ms a 0,84 ms mientras
verifica. Un recorrido por empresa y cuatro por proceso (la que sobra recibe 429 `VERIFICATION_BUSY`, no espera cola) y cada uno
tiene `AUDIT_VERIFY_TIMEOUT` (25 s por defecto, tope 28: por debajo del `WriteTimeout` de 30 s de `pkg/server`;
vencido, 504 `VERIFICATION_TIMEOUT`). A ese ritmo (máquina de
medición; la del servidor será más lenta, medirlo allí) 25 s alcanzan para unos 5 millones de filas por empresa; más
allá, la salida es verificar de forma incremental desde el último ancla verificado.

`hash_key_missing` y `hash_key_unknown` son problemas de configuración hasta que se demuestre lo contrario; el resto son
señal de manipulación. La respuesta **nunca** incluye la llave ni su identificador, y **no nombra filas de otra empresa**:
si la fila que rompe la cadena tiene otro `tenant_id` (una base con filas mezcladas, o un `tenant_id` editado) el
veredicto sigue en rojo pero sin `broken_id` ni `broken_seq`. La comparación de hashes es en tiempo constante.

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
6. **Guardar el ancla solo fuera de la base.** Es la meta (sección siguiente), pero el repositorio no puede dejarla
   hecha: necesita un destino externo y credenciales que hoy no existen.

## Ancla externa (decidida, pendiente)

**Lo que protege hoy el ancla.** La tabla, el evento y el log dan tres cosas: detectan el borrado de las últimas filas y
la reescritura de la cadena contra un atacante que **no pueda escribir también `audit.chain_anchors`** (el rol del
servicio no puede; el dueño de la base sí), y dejan la cabeza en un stream de NATS y en el log. Un ancla dentro de la
**misma base** no protege contra quien escribe en ambas tablas: borra las últimas filas y las últimas anclas. Presentarla
como protección completa sería falso.

**Lo que la completa.** Sacar la cabeza a un sistema que ese atacante no controle. Decisión: un consumidor de
`audit.chain.anchored` (evento ya publicado con todo lo necesario, así que no hay cambio de formato) que la entregue
fuera del servidor. Destinos válidos, por orden de preferencia:

1. Un resumen diario de las cabezas por correo a una dirección externa a la plataforma (`ALERT_EMAIL_TO`, la que ya usa el
   Alertmanager de `docs/adr/0005-entrega-de-alertas-con-alertmanager.md`). Un correo enviado no se puede borrar desde
   el servidor, y no necesita infraestructura nueva. Una alerta del Alertmanager cuando `audit` registre el error de
   ancla ausente cubre el aviso inmediato.
2. Un objeto con retención inmutable (S3 con Object Lock o equivalente) **fuera del host**. MinIO en el mismo host no
   sirve: el atacante que escribe en la base escribe ahí.

No se implementa porque necesita decidir el destino y darle credenciales que hoy no existen (el mismo bloqueo que la
caída total en el ADR 0005). Hasta entonces, `audit.chain.anchored` en JetStream y el log son la única copia fuera de la
base, y los dos viven en el mismo servidor.

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
* **`GET /integrity` es lineal** en el tamaño de la cadena, como antes; la versión 2 añade un HMAC por fila, sin coste
  apreciable frente a la lectura. El recorrido lee las filas en flujo (memoria acotada a una fila) y con el contexto de
  la petición (se cancela si el cliente se va), pero cada uno ocupa una conexión y lee la tabla entera de una empresa en
  una base que comparten todas, así que `audit` lo limita (2026-09-21, revisión de seguridad): **uno por empresa y
  cuatro a la vez por proceso** (`verificationGate` en `app`), 429 `VERIFICATION_BUSY` con `Retry-After: 30` al resto
  y corte a los 15 minutos. Los 30 s de `WriteTimeout` de los servicios hacen que una cadena de millones de filas no
  pueda contestar dentro de la petición: la salida es un trabajo asíncrono (ver "Mejoras futuras").
* **Rotación sin retirada**: cada llave retirada debe conservarse mientras existan filas con su `key_id`.

## Mejoras futuras (no implementadas)

* Consumidor de `audit.chain.anchored` que entregue la cabeza fuera del servidor (sección "Ancla externa").
* Puntos de control firmados con la llave activa cada cierto número de filas, para poder retirar llaves viejas.
* Métrica y regla de alerta de Prometheus por ancla ausente, en lugar de solo el log.
* Compactación de anclas antiguas.
* Verificación incremental (desde el último punto verificado) en lugar de recorrer toda la cadena.
* Verificación como trabajo asíncrono (`POST` que la lanza y `GET` que lee el resultado, con el cupo por empresa en
  Redis para que valga entre réplicas): hoy el cupo y el corte son por proceso y la respuesta debe caber en el
  `WriteTimeout` del servicio.
* Marcar el origen de cada apunte (`POST /logs` y `/logs/bulk` los escribe quien tenga `audit/logs/create`, con
  cualquier módulo y acción, a su propio nombre): hoy un apunte del API es indistinguible de uno del gateway o del bus.
