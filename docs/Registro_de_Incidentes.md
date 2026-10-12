# Registro de incidentes

Un incidente es cualquier fallo encontrado en producción, en la CI o en una prueba, y resuelto: una
vulnerabilidad, un servicio que se degrada, una prueba intermitente, un aviso que no avisa. Cada uno
deja aquí una entrada, en la misma tarea en la que se resuelve, aunque el arreglo ya esté explicado
en su commit o en otro documento. Este registro es el índice: responde qué pasó, por qué y qué
impide que vuelva, y apunta a donde está el detalle.

Si un incidente lleva a una decisión de arquitectura, la decisión va en un ADR (`docs/adr/`) y aquí
solo se enlaza. Las entradas más recientes van arriba.

## Plantilla

```markdown
### AAAA-MM-DD · Título corto, en términos del síntoma

| | |
|---|---|
| Detectado | Cómo y cuándo apareció (alerta, incidencia, prueba, usuario) |
| Síntoma | Lo que se veía |
| Impacto | Lo que sufrió el producto o el usuario, y durante cuánto tiempo. "Ninguno" si no lo hubo |
| Causa raíz | Por qué pasó, comprobado (no la primera hipótesis) |
| Solución | Qué se cambió, con su commit |
| Que no se repita | El check, la prueba o la alerta que falla si vuelve; o por qué no hay ninguno |
| Detalle | Documentos, ADR, incidencias |
| Desplegado | Fecha y commit en producción, o "no aplica" |
```

## Índice

| Fecha | Incidente | Impacto | Guardarrail |
|---|---|---|---|
| 2026-10-12 | [Las reservas de citas y los recordatorios fallaban con 503 por el límite de mail-directory](#2026-10-12--las-reservas-de-citas-y-los-recordatorios-fallaban-con-503-por-el-límite-de-mail-directory) | `make e2e-mail` en rojo en `main` (#29); nada llegó a producción | `make e2e-mail` y `ratelimit_test.go` |
| 2026-10-12 | [La CI quedó en rojo por 11 vulnerabilidades alcanzables de Go publicadas ese día](#2026-10-12--la-ci-quedó-en-rojo-por-11-vulnerabilidades-alcanzables-de-go-publicadas-ese-día) | CI en rojo unas 3 horas; despliegue retenido | `govulncheck` en la CI y Go atado a `go.mod` |
| 2026-10-12 | [`make e2e-mail` falló al construir unbound por un error TLS puntual del CDN de Alpine](#2026-10-12--make-e2e-mail-falló-al-construir-unbound-por-un-error-tls-puntual-del-cdn-de-alpine) | Ejecución nocturna en rojo y despliegue de motores retenido (#25) | `check-unbound-install-retry.sh` |
| 2026-10-05 | [Escribir una respuesta podía archivar el mensaje abierto](#2026-10-05--escribir-una-respuesta-podía-archivar-el-mensaje-abierto) | Mensajes archivados sin querer al responder en el webmail | `shortcuts.test.tsx` |
| 2026-10-04 | [La CI de integración no compilaba tras un puerto nuevo](#2026-10-04--la-ci-de-integración-no-compilaba-tras-un-puerto-nuevo) | main en rojo unos 30 min; despliegue retenido | `go vet -tags integration` en `make checks` |
| 2026-10-04 | [La recuperación de contraseña no servía a los buzones y no lo decía](#2026-10-04--la-recuperación-de-contraseña-no-servía-a-los-buzones-y-no-lo-decía) | Un usuario de buzón esperaba un correo que nunca llegaba | `MailboxRecoveryPage.test.tsx` y `make e2e-mail` |
| 2026-10-02 | [El 90 % de las llamadas a la API de JetStream fallaban](#2026-10-02--el-90--de-las-llamadas-a-la-api-de-jetstream-fallaban) | Ruido que tapaba errores reales | Prueba de integración |
| 2026-10-01 | [Eventos con 90 s de retraso en cada despliegue](#2026-10-01--eventos-con-90-s-de-retraso-en-cada-despliegue) | Retraso de eventos de 11 servicios en cada reinicio | `check-subscription-drain.sh` |
| 2026-10-01 | [La réplica de Dovecot no funcionaba y ensuciaba el registro](#2026-10-01--la-réplica-de-dovecot-no-funcionaba-y-ensuciaba-el-registro) | Un error por buzón en cada arranque; réplica rota en silencio | `make e2e-mail` (ADR 0018) |
| 2026-10-01 | [estado-produccion daba trabajo pendiente que no existía](#2026-10-01--estado-produccion-daba-trabajo-pendiente-que-no-existía) | Confusión al operar | `check-estado-produccion.sh` |
| 2026-10-01 | [El aviso de claves del despliegue listaba 186 nombres](#2026-10-01--el-aviso-de-claves-del-despliegue-listaba-186-nombres) | Una clave nueva obligatoria no se veía | `check-deploy-preflight.sh` |
| 2026-10-01 | [Conversaciones del webmail desordenadas](#2026-10-01--conversaciones-del-webmail-desordenadas) | Hilos en orden equivocado si dos mensajes compartían segundo | Prueba unitaria |
| 2026-10-01 | [`make e2e-mail` en rojo cuatro noches sin que nadie actuara](#2026-10-01--make-e2e-mail-en-rojo-cuatro-noches-sin-que-nadie-actuara) | El fallo siguiente llegó a producción | Incidencia `e2e-motores` y puerta en `deploy-ecr.sh` |
| 2026-10-01 | [Los avisos en vivo del webmail no llegaban](#2026-10-01--los-avisos-en-vivo-del-webmail-no-llegaban) | Sin avisos de correo nuevo durante cuatro días | `check-response-writers.sh` |
| 2026-10-01 | [Vulnerabilidades altas en las imágenes de los motores](#2026-10-01--vulnerabilidades-altas-en-las-imágenes-de-los-motores) | 7 CVE altas con arreglo publicado en producción | `check-motor-os-updates.sh` |

## Incidentes

### 2026-10-12 · "Unexpected Application Error! Hey developer" tras un despliegue de la web

| | |
|---|---|
| Detectado | El usuario, al entrar al webmail minutos después de un despliegue: `Failed to fetch dynamically imported module: .../assets/WebmailApp-CbAff20Y.js` |
| Síntoma | Pantalla en blanco con el error técnico del enrutador y el texto "Hey developer" |
| Impacto | Un usuario no podía entrar hasta recargar. Los registros del servidor mostraron el archivo entregado con 200 y sin ningún 4xx de esa IP: el fallo fue del lado del navegador |
| Causa raíz | No determinada al 100%: el servidor entregó el archivo, y en un Chrome limpio la misma ruta carga completa. Lo compatible con los datos es una pestaña, caché o service worker con una versión anterior de la web tras el redespliegue (los archivos llevan huella). Lo que sí era un defecto: la aplicación no tenía pantalla de error propia ni recuperación ante ese fallo |
| Solución | Un fallo de carga de módulo recarga la página una sola vez (`vite:preloadError` y `errorElement` de las rutas, con marca de hora en `sessionStorage` y ventana de 60 s contra bucles); si persiste, pantalla propia con "Recargar" e "Ir al inicio" y sin error técnico. Excepción de lint acotada a `staleChunk.ts` (una marca de hora, sin datos de sesión) |
| Que no se repita | `staleChunk.test.ts` (reconoce los mensajes de Chrome, Firefox y Safari; recarga una vez; sin almacenamiento no recarga) y `RouteErrorPage.test.tsx` (nunca muestra el error técnico) |
| Detalle | `web/src/lib/staleChunk.ts`, `web/src/layout/RouteErrorPage.tsx` |
| Desplegado | 2026-10-12, `c31a207` (web) |

### 2026-10-12 · El pase de automatización no podía recrear el gateway si iba por detrás del último despliegue

| | |
|---|---|
| Detectado | Al abrir el pase para revisar el webmail con el MCP de Chrome, a petición del usuario |
| Síntoma | `scripts/pase-automatizacion.sh on` escribió las variables en el `.env` y falló al recrear el gateway: `No such image: core-force-mail/gateway:1f7b494` |
| Impacto | Ninguno para los usuarios: compose abortó antes de tocar el contenedor y el gateway siguió sano en `828eb32`. El pase quedó escrito pero sin efecto hasta recrear |
| Causa raíz | `recrear_gateway` tomaba el tag de `.deployed-tag`, el del último despliegue. Un despliegue solo reconstruye los servicios que cambiaron, así que el gateway puede ir varios commits por detrás y la imagen de ese tag no existe en el servidor |
| Solución | Toma el tag de la imagen del gateway en marcha (`docker inspect app-gateway-1`) |
| Que no se repita | Sin guardarrail automático: el fallo solo aparece contra un servidor con servicios en tags distintos, que ninguna prueba local reproduce. El script falla antes de tocar nada y lo dice |
| Detalle | `docs/Plan_Proteccion_Frente_a_Bots.md`, capa 2 |
| Desplegado | No aplica (es un script que corre desde el puesto). Pase abierto 1 hora solo para la IP del usuario, usado y cerrado el 2026-10-12 |

### 2026-10-12 · La respuesta del soporte de un proveedor caía en Notificaciones y pasaba desapercibida

| | |
|---|---|
| Detectado | El usuario: la respuesta de Squarespace a su ticket y el correo de restablecer contraseña no estaban en Principal |
| Síntoma | Dos mensajes sin leer en la bandeja de entrada, pero en la pestaña Notificaciones |
| Impacto | Un correo esperado (la respuesta de un soporte) quedó fuera de la vista que el usuario mira siempre; no se perdió nada |
| Causa raíz | Las reglas deterministas de la bandeja inteligente: `no-reply@` por el remitente y `Auto-Submitted: auto-generated` que el sistema de tickets del proveedor pone también a las respuestas de sus agentes (comprobado en las cabeceras). Principal solo recogía lo que no cumple ninguna regla |
| Solución | Tres cambios en la clasificación: lo sin leer entra también en Principal (conserva su categoría y vuelve a su pestaña al leerlo); un mensaje que responde a algo enviado desde el dominio del buzón es siempre principal; y el botón "Siempre en Principal" por remitente (`mail.mailbox_primary_senders`) (`e74a11c` y el commit de los remitentes fijados) |
| Que no se repita | Pruebas de que la pestaña del servidor (IMAP SEARCH) y la clasificación del listado coinciden con las tres reglas, incluido el caso del remitente con dominio parecido; prueba de integración de la tabla (RLS, tope, borrado con el buzón). La primera versión pedía la lista al directorio en cada listado y el e2e vio 429 (el directorio limita a 120 por minuto por origen y el webmail es uno solo): ahora se cachea un minuto por buzón y un fallo 15 s, con pruebas que cuentan las lecturas |
| Detalle | `docs/Plan_Webmail_Innovador.md`, sección 3, punto 4 |
| Desplegado | 2026-10-12, `1f7b494`: migración de celda 17 aplicada a mano antes, luego `mail-directory`, `webmail` y `web`. Probado en producción con un buzón real: fijar y quitar un remitente mueve su correo de Notificaciones a Principal y lo devuelve |

### 2026-10-12 · Al cambiar de buzón, el webmail intentaba abrir un mensaje que no existía

| | |
|---|---|
| Detectado | El usuario, al entrar con otro buzón tras cerrar la sesión de `ann@thorre360.com` (log del webmail: `GET .../INBOX/messages/2` 200 y, tras el cierre y la entrada nueva, 404 en el mismo segundo) |
| Síntoma | Pantalla "No se pudo cargar la información: el mensaje ya no existe" nada más entrar, sin haber abierto nada |
| Impacto | Aviso rojo engañoso al cambiar de buzón en el mismo navegador; ningún correo se perdió |
| Causa raíz | `RequireWebmailSession` redirigía al acceso con `from = pathname + search`, y el `search` lleva `folder` y `uid` del mensaje abierto. Tras entrar, `returnPath` volvía a esa dirección aunque el buzón fuera otro |
| Solución | `closedByUser` en el estado del webmail: el cierre voluntario no recuerda la pantalla; la sesión caducada y los enlaces directos siguen volviendo a donde estaban (`7b22c1e`) |
| Que no se repita | `WebmailApp.test.tsx` (tras el cierre no se arrastra la dirección, sin sesión sí) y las pruebas del store; probado que la prueba falla sin el arreglo |
| Detalle | `web/src/pages/webmail/WebmailApp.tsx`, `web/src/webmail/store.ts` |
| Desplegado | 2026-10-12, `1f7b494` (web) |

### 2026-10-12 · Las reservas de citas y los recordatorios fallaban con 503 por el límite de mail-directory

| | |
|---|---|
| Detectado | Incidencia #29 (`make e2e-mail` en `d32e00d` y `dd71944`), reproducido en local |
| Síntoma | 9 fallos en el flujo de citas: la reserva pública respondía 503 `SERVICE_UNAVAILABLE`; en el log del webmail, `mail-directory respondió 429` |
| Impacto | `main` en rojo y despliegue retenido por la puerta de `make e2e-mail`. Sin impacto en producción: el cambio no se había desplegado |
| Causa raíz | Webmail pidió a mail-directory los remitentes fijados en Principal en cada listado de INBOX. Todas las llamadas del webmail llegan a mail-directory desde una misma IP, que tiene 120 peticiones por minuto, y ese cupo ya lo gastaban en gran parte los sondeos de recordatorios y envíos programados: un listado más por segundo agotaba el cupo y las reservas, los recordatorios y los envíos programados recibían 429 |
| Solución | Cache de los remitentes fijados por buzón en el caso de uso del webmail, de un minuto, invalidada al fijar o quitar y que recuerda 15 s un fallo (`fa6affc`); la prueba de Principal se ajustó al comportamiento nuevo (`d471f1e`) |; el cupo de mail-directory pasa a contarse por buzón, por ruta y por usuario según la llamada (`ratelimit.go`)
| Que no se repita | `make e2e-mail` (la reserva falla si mail-directory limita) y `services/mail-directory/ratelimit_test.go`: el cupo de la API interna ya no es por IP de origen sino por buzón (por ruta si no lleva buzón) y el del API con sesión, por usuario, como en mail-files y mail-dav, de modo que otra llamada por petición del webmail al directorio no agota el cupo de los demás |
| Detalle | `services/mail-directory/main.go` (`NewRateLimiter(120, time.Minute)`), `pkg/middleware/ratelimit.go` |
| Desplegado | 2026-10-12: la caché con `1f7b494` y el cupo por buzón de mail-directory con `595eb11` |

### 2026-10-12 · La CI quedó en rojo por 11 vulnerabilidades alcanzables de Go publicadas ese día

| | |
|---|---|
| Detectado | CI de `9d251ff` en adelante, paso «Vulnerabilidades alcanzables (Go)» (`govulncheck`), al ir a desplegar el port de mailcow |
| Síntoma | 11 avisos (GO-2026-6599 a 6617): `net/http`, `net/textproto`, `crypto/tls` y `html/template` de Go 1.26.8, y `golang.org/x/net` v0.58.0, con trazas alcanzables desde los servicios |
| Impacto | CI en rojo unas 3 horas; ningún despliegue salió con ello. En producción corrían binarios con la biblioteca estándar vulnerable hasta el despliegue de `828eb32` |
| Causa raíz | Aviso nuevo del día, no un cambio nuestro: Go publicó 1.26.9 y `x/net` v0.60.0 con los arreglos. La CI pedía `go-version: '1.26'` y el ejecutor usa la copia en caché (1.26.8), así que ni siquiera un `go.mod` al día evitaba compilar con la anterior |
| Solución | `go 1.26.9` en `go.mod`, `golang.org/x/net` v0.60.0 y `go-version-file: go.mod` en los flujos (`828eb32`); toda la plataforma reconstruida y desplegada |
| Que no se repita | Atar el compilador a `go.mod` hace que una imagen o un ejecutor atrasados fallen al construir en lugar de compilar con la biblioteca vulnerable; `govulncheck` en la CI sigue avisando de los nuevos |
| Detalle | `x/crypto` v0.57.0 tiene un aviso sin versión corregida, no alcanzado por nuestro código; revisar cuando haya arreglo |
| Desplegado | 2026-10-12, `828eb32` (plataforma completa y motores unbound, postfix-tlspol, postfix y rspamd) |

### 2026-10-12 · `make e2e-mail` falló al construir unbound por un error TLS puntual del CDN de Alpine

| | |
|---|---|
| Detectado | Incidencia #25, abierta por `mail-engines.yml` (ejecución programada sobre `c738fc1`) |
| Síntoma | `compose build` falló: `apk add` desde `edge` dio `TLS: unspecified error` al bajar el índice y después `unbound-1.25.2-r1 breaks: world[unbound>=1.26.1-r0]` |
| Impacto | Una ejecución nocturna en rojo; `deploy-mail.sh` y `deploy-ecr.sh` retenidos hasta el siguiente verde. Sin impacto en producción |
| Causa raíz | Fallo de red transitorio contra `dl-cdn.alpinelinux.org`: sin el índice de `edge`, apk resuelve contra el estable (1.25.2), que no cumple la versión mínima fijada. Las seis ejecuciones anteriores pasaron y `edge` publica 1.26.1-r0, así que no es un defecto de la fijación |
| Solución | La instalación de unbound desde `edge` se reintenta 6 veces con espera creciente y alternando entre `dl-cdn.alpinelinux.org` y `uk.alpinelinux.org`, que sirven el mismo índice firmado (`9d251ff`, ampliado después con el espejo) |
| Que no se repita | `ops/scaffold/check-unbound-install-retry.sh` (en `make checks`): ejecuta la instrucción con un `apk` falso que falla, y comprueba que reintenta, alterna de espejo y que la construcción falla si nunca instala; probado con una instalación sin reintento. Si caen los dos espejos a la vez, la incidencia `e2e-motores` lo avisa |
| Detalle | `deploy/mail/UPSTREAM.md`, fila de `unbound/Dockerfile` |
| Desplegado | No aplica (la imagen no cambia de contenido; se reconstruye en el próximo despliegue de motores) |

### 2026-10-04 · La CI de integración no compilaba tras un puerto nuevo

| | |
|---|---|
| Detectado | CI de `12e9ce1`, trabajo «Integracion» |
| Síntoma | `services/webmail/internal/app/integration_test.go:371`: `noSecurity` no implementa `ports.SecurityDirectory` (falta `RecoverPassword`) |
| Impacto | main en rojo unos 30 minutos; ningún despliegue salió con ello |
| Causa raíz | Los dobles de las pruebas de integración llevan la etiqueta `integration` y solo se compilan en `make test-integration` (con docker) o en la CI. `make checks` corría `go vet ./...` sin la etiqueta, así que un método nuevo en un puerto pasaba los checks locales |
| Solución | El doble implementa el método, y `make checks` corre además `go vet -tags integration ./...` (5 s, sin docker) |
| Que no se repita | `make checks`: probado quitando el arreglo, falla con el mismo error |
| Detalle | — |
| Desplegado | No aplica (CI y checks) |

### 2026-10-05 · Escribir una respuesta podía archivar el mensaje abierto

| | |
|---|---|
| Detectado | Prueba en navegador real contra producción (buzón `it@mentorenergy.uk`) al revisar la respuesta dentro del mensaje: aparecieron avisos de «mensajes movidos a Archivo» mientras se escribía |
| Síntoma | Tras un clic en un hueco de la respuesta (entre campos, no en el texto), cada «e» tecleada archivaba el mensaje abierto y «#» lo habría borrado |
| Impacto | Mensajes archivados sin querer al responder dentro del lector, desde `fda805c` (2026-10-04) hasta este arreglo. En la ventana flotante el riesgo ya existía, pero era menor: estaba fuera del lector |
| Causa raíz | Los atajos de una letra solo se callan si el foco está dentro de la zona de redacción (`data-keyboard-owner`) o en un campo. Un clic en un elemento que no recibe foco lo pasa al ancestro enfocable más cercano, que era el `main` del webmail, fuera de la redacción: la tecla llegaba al atajo de archivar. Comprobado con un registro de teclas en la página: destino `MAIN` y una petición `batch` de archivado por cada «e» |
| Solución | La respuesta en línea y la ventana reciben ese foco (`tabIndex=-1`), y los atajos tampoco actúan si el foco cae en un contenedor que envuelve una redacción abierta (`4713c0f`) |
| Que no se repita | `web/src/webmail/shortcuts.test.tsx`: un clic en un hueco de la redacción deja el foco dentro y «e» no archiva; con el foco caído en un contenedor de la redacción tampoco |
| Detalle | `web/src/webmail/shortcuts.ts` |
| Desplegado | 2026-10-05, con `2b7a256` (web); la respuesta dentro del recuadro del mensaje, en `7db4d1e` |

### 2026-10-04 · La recuperación de contraseña no servía a los buzones y no lo decía

| | |
|---|---|
| Detectado | Prueba manual en producción con `asotoc@core-force.com`: la pantalla respondió y no llegó nada |
| Síntoma | «Recuperar contraseña» respondía «Si el correo está registrado, recibirás un enlace…» y identity registraba «correo no registrado» |
| Impacto | Quien solo tiene buzón (la mayoría de los usuarios del webmail) no podía recuperar su contraseña por sí mismo, y el mensaje le hacía esperar un correo que nunca llegaba. Desde que existe el inicio de sesión único |
| Causa raíz | El inicio de sesión es uno solo para buzones (mail-auth) y cuentas de la consola (identity), pero el enlace de recuperación solo existe en identity. El mensaje es idéntico exista o no el correo, a propósito (contra la enumeración), así que ocultaba también este caso. Un buzón tampoco podría recibir el enlace: es justo el buzón al que no puede entrar |
| Solución | Recuperación del buzón sin sesión con un código TOTP y uno de recuperación a la vez, en una transacción de mail-directory que cierra las sesiones, borra las contraseñas de aplicación y deja rastro en Auditoría; la pantalla de recuperación explica a quien tiene buzón qué hacer (`12e9ce1`) |
| Que no se repita | `MailboxRecoveryPage.test.tsx` y las unitarias de mail-directory y webmail; `make e2e-mail` recorre la recuperación contra los motores reales |
| Detalle | `docs/Plan_Webmail_Seguridad.md`, sección 7 |
| Desplegado | 2026-10-05, `2b7a256` (mail-directory, webmail, audit, gateway, web; `AUDIT_SUBJECTS` del servidor con el subject nuevo) |

### 2026-10-02 · El 90 % de las llamadas a la API de JetStream fallaban

| | |
|---|---|
| Detectado | Revisando el monitor de NATS (`/jsz`) tras otro incidente |
| Síntoma | 346 000 de 382 000 llamadas a la API con error en siete días; en el último minuto, el 100 % |
| Impacto | Ninguno funcional. El contador de errores de NATS no servía: un error real habría pasado inadvertido |
| Causa raíz | La métrica `events_dlq_messages` pregunta por el stream `EVENTS_DLQ` en cada recolección de Prometheus (11 servicios, cada 30 s). El stream solo se creaba con el primer evento abandonado, que nunca llegó; cada pregunta por un stream inexistente cuenta como error en el servidor |
| Solución | `ensureDLQStream` crea `EVENTS_DLQ`, de forma idempotente, al atar cada consumidor durable (`b81fba3`) |
| Que no se repita | `TestAtarUnDurableCreaLaDLQ` (`pkg/events/bus_integration_test.go`), contra NATS real |
| Detalle | `docs/Operacion_Despliegue.md`, sección 10 |
| Desplegado | 2026-10-02, `b81fba3`: 0 errores en las llamadas siguientes |

### 2026-10-01 · Eventos con 90 s de retraso en cada despliegue

| | |
|---|---|
| Detectado | Una prueba de integración de audit falló en la CI (59 apuntes de 60) y pasó al relanzarla |
| Síntoma | Prueba intermitente, reproducida en local aproximadamente 1 de cada 15 ejecuciones |
| Impacto | En cada despliegue o reinicio, parte de los eventos que recibían los consumidores durables de 11 servicios (auditoría, analítica, facturación, rebotes y quejas, supresiones...) llegaba hasta 90 s tarde. No se perdía ninguno |
| Causa raíz | Los consumidores se paraban con `Drain()` de nats.go, que vuelve en el acto mientras la baja sigue por detrás. Lo que el servidor empujaba en ese hueco quedaba sin confirmar hasta `AckWait` (90 s). La primera hipótesis, que un evento publicado sin confirmación se perdía, resultó falsa: el diagnóstico mostró el stream completo y dos mensajes entregados al consumidor que se estaba parando |
| Solución | `events.DrainSubscriptions` hace el drain y espera hasta 5 s a los mensajes en vuelo; los 11 servicios lo usan, fuera de su mutex (`587dcba`). La prueba pasó de 4 a 7 fallos por cada 60 a 100 de 100 |
| Que no se repita | `ops/scaffold/check-subscription-drain.sh` (en `make checks`) prohíbe un `.Drain()` suelto fuera de `pkg/events`; pruebas de integración del helper |
| Detalle | `docs/Operacion_Despliegue.md`, apagado de los servicios Go |
| Desplegado | 2026-10-02, `587dcba`: 56 de 56 consumidores con suscriptor y sin mensajes pendientes tras reiniciar los 25 servicios |

### 2026-10-01 · La réplica de Dovecot no funcionaba y ensuciaba el registro

| | |
|---|---|
| Detectado | Verificando un despliegue de Dovecot |
| Síntoma | En cada arranque, un `doveadm client isn't allowed to use command: sync` por buzón |
| Impacto | Ruido en el registro. Además, si alguien configuraba `MAIL_REPLICA_IP`, la réplica fallaba en silencio: se habría creído tener una copia que no existía |
| Causa raíz | La configuración heredada de mailcow dejaba el `replicator` siempre en marcha. `doveadm_allowed_commands`, que limita la API de doveadm de mail-security a `kick` y `auth cache flush`, es global en Dovecot 2.3 (comprobado: ni `local {}` ni `remote {}` lo filtran por conexión) y rechazaba su `sync`. Permitirlo daría a la clave de la API un `sync` de cualquier buzón hacia un servidor ajeno |
| Solución | Primero se apagó el replicator sin réplica (`11a7cee`). Después se retiró la réplica entera y el contenedor se niega a arrancar con `MAIL_REPLICA_IP` o `DOVEADM_REPLICA_PORT` (`9c4e22c`) |
| Que no se repita | `make e2e-mail` comprueba que no se carga `replication`, que no hay replicator, que no hay `sync` rechazados y que no arranca con réplica configurada |
| Detalle | `docs/adr/0018-dovecot-sin-replica.md`; `deploy/mail/UPSTREAM.md`; `deploy/mail/README.md` |
| Desplegado | 2026-10-01, `9c4e22c` |

### 2026-10-01 · estado-produccion daba trabajo pendiente que no existía

| | |
|---|---|
| Detectado | Al alinear el estado después de un despliegue |
| Síntoma | `scripts/estado-produccion.sh` decía "queda trabajo por desplegar" y `scripts/deploy-ecr.sh` respondía "nada que desplegar" |
| Impacto | Confusión al operar; un veredicto que no se puede satisfacer acaba ignorado |
| Causa raíz | Cualquier commit posterior al desplegado contaba como pendiente, aunque solo tocara documentación, pruebas o un motor ya desplegado |
| Solución | La lista de commits pasa a ser informativa; el veredicto sale de migraciones, servicios, ficheros y motores (`e0dee18`) |
| Que no se repita | Caso nuevo en `ops/scaffold/check-estado-produccion.sh` |
| Detalle | Cabecera de `scripts/estado-produccion.sh` |
| Desplegado | No aplica (herramienta local) |

### 2026-10-01 · El aviso de claves del despliegue listaba 186 nombres

| | |
|---|---|
| Detectado | Leyendo la salida de un despliegue |
| Síntoma | `AVISO: claves de .env.example que .env no tiene (186)` en cada despliegue |
| Impacto | Una clave nueva y obligatoria se habría perdido entre las demás |
| Causa raíz | Contaba los secretos (viven en el almacén, nunca en `.env`) y las claves opcionales con valor por defecto, que llevan meses ausentes a propósito |
| Solución | `claves-env.sh` admite `--excluir` (secretos) y `--nuevas` (claves aparecidas desde lo desplegado); solo esas se listan y el resto va en una línea (`04289c6`) |
| Que no se repita | Casos nuevos en `ops/scaffold/check-deploy-preflight.sh` |
| Detalle | `docs/Operacion_Despliegue.md`, sección 5 |
| Desplegado | 2026-10-01, `04289c6` |

### 2026-10-01 · Conversaciones del webmail desordenadas

| | |
|---|---|
| Detectado | `make e2e-mail` falló en la CI con tres mensajes enviados en el mismo segundo |
| Síntoma | La conversación salía como `INBOX INBOX Sent` en lugar de `INBOX Sent INBOX` |
| Impacto | Respuestas muy rápidas (un autorresponder, por ejemplo) se mostraban en el orden de las carpetas y no en el de la conversación |
| Causa raíz | Se ordenaba solo por la cabecera `Date`, de resolución de segundos |
| Solución | Desempate por la profundidad en la cadena de `In-Reply-To`, con corte de ciclos (`c67ca9a`) |
| Que no se repita | `TestOrderConversationDesempataPorLaCadenaDeRespuestas` |
| Detalle | `services/webmail/internal/app/insight.go`, `orderConversation` |
| Desplegado | 2026-10-01, `c67ca9a` |

### 2026-10-01 · `make e2e-mail` en rojo cuatro noches sin que nadie actuara

| | |
|---|---|
| Detectado | Al intentar desplegar motores: `deploy-mail.sh` se negaba porque el flujo llevaba días fallando |
| Síntoma | `mail-engines.yml` fallaba cada noche desde el 2026-09-27 |
| Impacto | El fallo que detectaba (los avisos del webmail, entrada siguiente) llegó a producción, y los motores quedaron sin poder desplegarse |
| Causa raíz | Un fallo del flujo solo generaba el correo de GitHub, y desplegar la plataforma no exigía esa prueba en verde (solo los motores) |
| Solución | El flujo abre la incidencia `e2e-motores`, asignada, al fallar en main, y la cierra con el siguiente verde; `deploy-ecr.sh` exige `make e2e-mail` en verde para los servicios que esa prueba cubre (`837421f`) |
| Que no se repita | La propia incidencia (probada: se abrió la #23 y se cerró sola) y la puerta, probada en `ops/scaffold/check-deploy-mail.sh` |
| Detalle | `docs/Operacion_Despliegue.md`, sección 12 |
| Desplegado | 2026-10-01 |

### 2026-10-01 · Los avisos en vivo del webmail no llegaban

| | |
|---|---|
| Detectado | Investigando por qué `make e2e-mail` llevaba días en rojo |
| Síntoma | El flujo SSE de avisos del webmail no entregaba nada hasta cerrarse |
| Impacto | Del 2026-09-27 al 2026-10-01, sin avisos de correo nuevo en el webmail; la bandeja se actualizaba igual por el sondeo |
| Causa raíz | El guardia de sondeo del gateway (`39f518a`, protección frente a bots, fase 3.2) envolvía toda respuesta de `/api/v1` en `statusRecorder`, que no tenía `Unwrap`: el proxy no podía vaciar el búfer |
| Solución | `Unwrap()` en `statusRecorder` (`e976532`) |
| Que no se repita | `ops/scaffold/check-response-writers.sh` (todo envoltorio de `http.ResponseWriter` tiene `Unwrap`) y `TestElGuardiaNoRetieneUnFlujoSSE` |
| Detalle | `ops/scaffold/README.md` |
| Desplegado | 2026-10-01, `c67ca9a` |

### 2026-10-01 · Vulnerabilidades altas en las imágenes de los motores

| | |
|---|---|
| Detectado | Incidencia #22, abierta por el escaneo mensual `imagenes-motores.yml` |
| Síntoma | 7 CVE altas con arreglo publicado: `libpcre2` en Postfix, Rspamd y postfix-tlspol; `setuptools`, `urllib3` y `msgpack` en netfilter |
| Impacto | Paquetes vulnerables en producción; plazo comprometido de 14 días |
| Causa raíz | La imagen `debian:trixie-slim` de Docker Hub va por detrás de `trixie-security`, y `apt-get install` no actualiza lo que la base ya trae. En netfilter quedaba una copia de `pip`, con sus librerías embebidas, por desinstalarlo con `--ignore-installed` |
| Solución | `apt-get upgrade -y` en las imágenes Debian y `pip` solo durante la construcción en netfilter (`38993ff`); `apk upgrade --no-cache` en las imágenes Alpine (`83b2fb0`) |
| Que no se repita | `ops/scaffold/check-motor-os-updates.sh` (en `make checks`); el escaneo mensual sigue abriendo una incidencia por cada vulnerabilidad nueva |
| Detalle | `deploy/mail/UPSTREAM.md`, sección 9 |
| Desplegado | 2026-10-01, todos los motores; la #22 la cerró el escaneo relanzado |
