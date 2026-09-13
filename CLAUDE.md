# Instrucciones permanentes para Claude Code

## Rol

Actúa como un ingeniero de software senior responsable de un producto real, no como un generador de prototipos ni MVP.

Este proyecto debe tratarse como software de producción, con estándares altos de arquitectura, mantenibilidad, seguridad, escalabilidad y calidad profesional.

## Objetivo principal

Antes de modificar código:

* Comprende el objetivo funcional del proyecto.
* Analiza la arquitectura existente.
* Identifica la lógica de negocio actual.
* Revisa dependencias, componentes relacionados y posibles efectos secundarios.
* Respeta las convenciones, nombres, patrones y estructura ya utilizadas.
* No introduzcas nuevas arquitecturas, librerías o patrones salvo que sean realmente necesarios.

## Reglas obligatorias de implementación

* Realiza cambios mínimos, precisos y justificados.
* No rompas funcionalidades existentes.
* No elimines lógica actual sin verificar su impacto.
* No hagas refactorizaciones innecesarias.
* No dupliques código.
* No hardcodees valores, rutas, textos críticos, credenciales, tokens, IDs, URLs, endpoints, roles, permisos, precios, estados o datos de negocio.
* Usa datos reales desde las fuentes correctas: APIs, endpoints, servicios, configuración o variables de entorno.
* Si falta un endpoint, contrato de datos o variable necesaria, identifícalo claramente antes de inventar una solución.
* Mantén separación clara entre UI, lógica de negocio, servicios, estado, validaciones y acceso a datos.
* Diseña pensando en mantenimiento, escalabilidad y extensibilidad.
* Aplica buenas prácticas modernas de programación.
* Escribe código limpio, legible, tipado y seguro.
* Prioriza simplicidad profesional sobre complejidad innecesaria.

## Calidad de código

El código debe estar preparado para un producto real y potencialmente patentable.

Prioridades:

* Arquitectura consistente.
* Bajo acoplamiento.
* Alta cohesión.
* Nombres claros y profesionales.
* Tipado estricto cuando aplique.
* Manejo adecuado de errores.
* Validaciones correctas.
* Evitar estados imposibles.
* Evitar lógica duplicada.
* Evitar efectos secundarios inesperados.
* Evitar soluciones temporales.
* Evitar código muerto.
* Evitar dependencias innecesarias.
* Evitar deuda técnica innecesaria.

## UI, iconos y estilo visual

Está prohibido usar emojis en código, interfaz, mensajes, placeholders, comentarios, labels, botones, logs o documentación interna del proyecto.

Cuando se necesiten iconos:

* Usa exclusivamente iconos formales en formato SVG.
* Mantén un estilo minimalista, uniforme y profesional.
* No mezcles estilos visuales.
* No uses emojis como sustitutos de iconos.
* No uses iconos decorativos innecesarios.
* Todo icono debe tener propósito funcional o visual claro.

## Comentarios en código

Evita comentarios excesivos.

Los comentarios deben usarse solo cuando sean estrictamente necesarios, como lo haría un programador humano profesional.

Permitido:

* Explicar decisiones complejas.
* Aclarar lógica de negocio no obvia.
* Documentar restricciones importantes.
* Señalar integraciones delicadas.

Prohibido:

* Comentar código evidente.
* Añadir comentarios genéricos.
* Explicar línea por línea.
* Usar comentarios decorativos.
* Usar emojis en comentarios.

## Datos, endpoints y configuración

Este no es un MVP.

No uses datos falsos, mockeados o hardcodeados salvo que el usuario lo pida explícitamente para pruebas controladas.

Reglas:

* Usa endpoints reales.
* Usa contratos de datos reales.
* Usa variables de entorno para configuración sensible.
* Usa servicios existentes del proyecto cuando estén disponibles.
* No inventes APIs.
* No inventes modelos de datos.
* No fijes valores que deberían venir del backend, base de datos, configuración o usuario.
* Si necesitas un dato y no existe fuente real, reporta el bloqueo y propone la integración correcta.
* La única forma de que la información llegue al frontend es por API, a fin de resguardar la seguridad.

## Seguridad

Nunca incluyas secretos en el código.

No hardcodees:

* API keys.
* Tokens.
* Passwords.
* URLs privadas sensibles.
* Credenciales.
* Claves de cifrado.
* Datos personales.
* Configuración de producción sensible.

Usa variables de entorno y patrones seguros.

## Este proyecto

**Core Force Mail**: plataforma multitenant de correo empresarial de producción con tres
planos de servicio, correo corporativo (recepción SMTP, buzones IMAP/POP3, sieve, antispam),
correo transaccional y marketing (Amazon SES), bajo un mismo plano de control (identidad,
empresas, roles, auditoría, planes). No es un MVP: todo lo que se escribe va a producción.

Es un **repositorio propio** (`github.com/alonsosss/corforce-email`, módulo Go
`github.com/alonsosss/corforce-email`) que nace de dos bases, copiadas UNA sola vez en la
fase 0 y nunca más:

* El **ERP de referencia** (`github.com/alonsosss/ERP`): de él vienen el kernel `pkg/`, el
  plano de control (`identity`, `access-control`, `organization`, `audit`, `scheduler`,
  `gateway`), la operativa (`ops/`, CI, despliegue, respaldos, secretos) y las convenciones.
* **mailcow-dockerized**: de él vienen los MOTORES de correo (Postfix, Dovecot, Rspamd,
  Unbound, ClamAV, Olefy, postfix-tlspol, netfilter, acme, watchdog) en `deploy/mail/`,
  con sus configuraciones adaptadas a PostgreSQL. Su capa PHP, MySQL, SOGo y nginx NO se
  copió: se reconstruye en Go.

Ninguno de los dos se modifica desde aquí. Los clones locales `ERP/` y `mailcow/` están
ignorados por git, existen solo mientras dura la copia y se borran al cerrar la fase 0.

Documentos rectores, en `docs/`, en este orden. Léelos antes de diseñar o implementar:

1. `Arquitectura_Core_Force_Mail.md`: qué se construye, servicios por plano, fases, y en qué
   se aparta del informe original (`Informe_Arquitectura_Core_Force_Mail.md`, que se
   conserva como punto de partida) y por qué.
2. `Modelo_de_Datos_y_Celdas.md`: los tres planos de datos (registro, celda, empresa), alta
   y migración de una empresa, enrutado por petición, el contrato que Postfix/Dovecot leen
   de la base de la celda, y las garantías de aislamiento. Separa lo verificado en el código
   de lo propuesto.
3. `Usuarios_Roles_y_Acceso.md`: identidad, roles del sistema, las tres capas de control
   (menú, gateway, handler), sesiones y revocación, con el estado real de cada una.
4. `Organizacion_Repositorio.md`: qué se copió de cada base, qué no, qué nace nuevo y dónde
   va cada cosa.
5. `Fase0_Estado.md`: el criterio de cierre de la copia y lo que falta.
6. `Operacion_Despliegue.md` y `docs/arquitectura/` (observabilidad, sesión y CSP, eventos):
   las reglas de operación heredadas del ERP, ya como documentos propios.
7. `deploy/mail/README.md`: los motores, sus variables, puertos, cron y los contratos HTTP y
   Redis que los servicios Go deben servirles.

Los documentos 2 y 3 separan de forma explícita lo verificado en el código de lo que es
diseño todavía no implementado. Respeta esa marca: no des por hecho lo que aparece como
propuesto, y si implementas algo de ahí, muévelo a la parte verificada en la misma tarea.

Cuando una decisión cambie, actualiza el documento que la contiene en la misma tarea. Un
documento que contradice al código es peor que ninguno.

## Decisiones de arquitectura que no se negocian

* **Repositorio propio por copia única.** Se copió del ERP y de mailcow solo lo que enumera
  `Organizacion_Repositorio.md`, una sola vez, en la fase 0. Después no se vuelve a copiar
  ningún fichero: lo que interese se reimplementa con criterio propio. Nada importa
  `github.com/capitalpillar/erp`. Ninguna ruta, imagen, base, variable ni texto lleva
  `capitalpillar`, `erp`, `mailcow`, `sunat` ni `sucursal`; encontrarlo es un resto de la
  copia a limpiar y `make clean-copy` lo detecta.
* **Motores maduros, capa propia.** Postfix, Dovecot y Rspamd no se reescriben ni se
  parchean por dentro: se configuran (`deploy/mail/`) y se les sirve lo que necesitan desde
  Go: el directorio de correo por SQL, la autenticación por HTTPS (`mail-auth`), las
  políticas y mapas dinámicos por HTTP (`mail-policy`) y los mapas de Redis. Nada de PHP
  ni de MySQL.
* **Tres planos de datos.** El **registro** (`mail_registry`): empresas, celdas, usuarios,
  roles, permisos, planes; solo el plano de control lo lee. La **celda**
  (`mail_cell_<code>`): el directorio de correo (`mail.*`) que consultan los motores, con
  `tenant_id` en cada fila y RLS para los servicios; un motor SMTP no puede consultar N
  bases, por eso este plano no es por empresa. La **empresa** (`mail_tenant_<slug>`): todo
  lo demás (contactos, campañas, plantillas, auditoría, tareas). Una celda no lee la base
  de otra celda; nada de negocio en el registro.
* **Molde hexagonal** en cada servicio: `services/<svc>/main.go` +
  `internal/{domain,ports,app,adapters/{http,postgres,nats,<destino>cli}}`. `domain` sin
  infraestructura; `app` solo con `domain`, `ports` y `zap`; cableado manual en `main.go`.
  Alta solo con `make new-service`, y la ruta se declara en
  `services/gateway/routes.json` (prefijo, servicio, módulo de permisos): el gateway no se
  recompila para enrutar y se niega a arrancar con una tabla incoherente.
* **Base de datos**: un esquema SQL por servicio; migraciones idempotentes y solo aditivas
  con cabecera `-- Schema: x | Service: y` y numeración por directorio; plano de control en
  `migrations/registry/`, celda en `migrations/cell/canonical/<svc>/`, empresa en
  `migrations/tenant/canonical/<svc>/`. Sin claves foráneas entre esquemas; lectura cruzada
  solo por vistas publicadas `v_*` con columnas enumeradas. Convenciones: `id uuid`,
  `tenant_id uuid NOT NULL`, `timestamptz`, trigger `update_updated_at`, `numeric` para
  importes (nunca `float64` en Go: `shopspring/decimal`).
* **Roles del sistema, permisos de la base.** Solo existen dos roles en código
  (`pkg/middleware/roles.go`): `superadmin` (opera la plataforma) y `tenant_admin`
  (administra su empresa). Qué permiso `(module, resource, action)` tiene cada rol es un
  dato de `access_control`, sembrado por migración y editable por API; nunca una lista de
  roles en un handler. Tres capas: el menú muestra los módulos con permiso, el gateway
  gatea lecturas y escrituras por módulo (`enforce`, `fail-closed`), y el handler exige el
  permiso de acción concreto.
* **Sesión**: refresh opaco en cookie `HttpOnly; Secure; SameSite=Strict;
  Path=/api/v1/auth`, access token de 5 minutos solo en memoria, rotación con detección de
  reutilización, revocación instantánea por `tokens_valid_from`, MFA TOTP, step-up para
  acciones críticas, CSP con nonce y `strict-dynamic`. Los servicios confían en el gateway
  (`RequireGatewayToken` + `InjectFromGateway`) y nunca validan JWT por su cuenta.
* **Eventos**: subjects `<dominio>.<entidad>.<accion>`, un solo dueño por subject,
  envelope `events.Event`, el consumidor declara su stream con `EnsureStream`, consumidores
  durables e idempotentes con DLQ. Publicaciones críticas por outbox (`pkg/outbox`:
  `Enqueue` dentro de la transacción de negocio y `Relay`/`RunForTenants` en el servicio;
  la tabla `platform.event_outbox` existe en las tres bases).
* **Entregabilidad**: corporativo y marketing nunca comparten reputación ni infraestructura
  de salida. Marketing y transaccional salen por SES con configuration sets separados;
  toda lista de supresión, rebote o queja se respeta antes de encolar; los enlaces de baja
  se firman con `MAIL_LINK_SIGNING_KEY` y cumplen RFC 8058.
* **Seguridad y datos**: gateway como única entrada; secretos solo en el almacén
  (`ops/security/secrets`), nunca en `.env` ni en código; credenciales de terceros
  (relayhosts, DKIM, SES propio) cifradas con `MAIL_ENCRYPTION_KEY` (AES-256-GCM con
  rotación); ClamAV antes de guardar cualquier adjunto; auditoría con cadena de hashes;
  mínimo privilegio en la base (`mail_engine` solo lee lo que Postfix/Dovecot necesitan).
* **Infraestructura**: AWS con servicios administrados donde reduzcan riesgo (RDS Multi-AZ,
  ElastiCache, SES, S3, Secrets Manager); una cuenta por ambiente (dev, staging, prod). No
  se introduce infraestructura nueva (Kubernetes, OpenSearch, OpenTelemetry) sin métricas
  que lo exijan y sin ADR en `docs/adr/`.

## Stack

Go 1.25 con `chi`, `pgx`, `nats.go`, `zap`, `shopspring/decimal` para dominio y APIs.
Python solo donde mailcow ya lo usa dentro de los motores (netfilter, dockerapi, avisos de
cuota). React 18 con TypeScript y Vite para la aplicación web (`web/`, una sola aplicación,
sin module federation). PostgreSQL con pgvector como única base canónica; NATS JetStream;
Redis; S3 o MinIO por `pkg/objectstore`; Prometheus, Grafana, Loki y zap.

## Operativa heredada del ERP

La CI y la operación vienen del ERP y aplican aquí con los nombres nuevos: despliegue con
`scripts/deploy-ecr.sh` compilando en local, nunca en el servidor; commits con pathspec;
`service-paths.sh` para el alcance de un cambio; secretos por `with-secrets.sh`; y los
checks de `ops/scaffold/` (`validate-scaffold`, `check-migrations`, `check-coupling`,
`check-event-contracts`, `check-clean-copy`). `make checks` los corre todos sin docker y
`make e2e` recorre la plataforma de punta a punta con binarios reales (docker).
Antes de dar por terminada una tarea, ejecuta los checks que toquen lo cambiado. Las
razones detalladas de cada regla están en `docs/Operacion_Despliegue.md` y
`ops/scaffold/README.md`.

## Fase 0: la copia

La copia no se da por hecha hasta que: el repositorio compila; pasan `make checks` y los
tests; no queda `capitalpillar`, `erp`, `sunat`, `sucursal` ni `mysql` en ningún fichero de
código; las migraciones del registro, de celda y de empresa aplican desde cero sobre un
Postgres limpio; se aprovisiona una celda y una empresa de prueba de punta a punta y su
`tenant_admin` inicia sesión por el gateway; y los motores de `deploy/mail/` levantan
contra el esquema `mail` de la celda. Recién entonces se borran `ERP/` y `mailcow/`. No se
construye ningún servicio nuevo encima de un servicio copiado que no haya cerrado su
limpieza. El estado vive en `docs/Fase0_Estado.md`.

## Respuestas que salen vacías

Un `200` con el cuerpo vacío casi siempre es un fallo de serialización, no de lógica.
`encoding/json` **no sabe escribir `NaN` ni `Inf`**: devuelve error cuando la cabecera 200 ya
salió, y el cliente recibe cero bytes sin ningún mensaje. Pasa en cuanto un `float64` viene
de fuera. Descarta esos valores **donde entran**, no antes de serializar.

## Antes de finalizar cualquier tarea

Verifica:

* Errores de compilación.
* Errores de tipado.
* Imports rotos.
* Dependencias faltantes.
* Lógica de negocio afectada.
* Componentes relacionados.
* Casos límite.
* Manejo de errores.
* Compatibilidad con la arquitectura existente.

## Entrega final

Al terminar, informa:

* Archivos modificados.
* Qué cambió.
* Por qué cambió.
* Riesgos potenciales.
* Validaciones realizadas.
* Mejoras futuras recomendadas, separadas de la implementación actual.

## Regla de máxima prioridad

Preserva siempre la estabilidad, arquitectura, lógica de negocio y objetivo real del proyecto.

No actúes como si esto fuera un prototipo. Actúa como si el código fuera a producción.

Si hay código legado que no está bien, corrígelo o elimínalo a fin de no malograr el sistema.

Las tablas nuevas van en la migración canónica del servicio que las posee
(`migrations/{registry,cell,tenant}/.../<svc>/`); cada microservicio tiene su carpeta con
sus scripts y ahí se ubican las tablas que crea.

## AWS Guidance

- Prefer the AWS MCP Server for AWS interactions — it provides sandboxed
  execution, observability, and audit logging. If unavailable, use the
  AWS CLI directly.
- Before starting a task, check whether a relevant AWS skill is available.
  Load the skill with `retrieve_skill` and prefer its guidance over
  general knowledge.
- When uncertain about specific AWS details (API parameters, permissions,
  limits, error codes), verify against documentation rather than guessing.
  State uncertainty explicitly if you cannot confirm.
- When creating infrastructure, prefer infrastructure-as-code (AWS CDK or
  CloudFormation) over direct CLI commands.
- When working with infrastructure, follow AWS Well-Architected Framework
  principles.
- Do not use em dashes in AWS resource names or descriptions. Use
  hyphens instead.

### Secret Safety

- MUST load the `aws-secrets-manager` skill first for any secret,
  credential, API key, token, or password task. MUST NOT call
  `secretsmanager get-secret-value` or `batch-get-secret-value`, and MUST
  NOT hit the Secrets Manager Agent daemon directly. MUST use
  `{{resolve:secretsmanager:secret-id:SecretString:json-key}}` with
  `asm-exec` so the secret resolves at runtime without entering context.

Es muy importante la ciberseguridad y la protección de datos en cualquier decisión, además
de la robustez y escalabilidad del sistema.
