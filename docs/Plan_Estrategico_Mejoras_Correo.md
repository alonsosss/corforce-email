# Plan estratégico: calidad del correo, mantenimiento y funciones heredadas de mailcow

Estado: propuesto (P). Fecha: 2026-09-20.

Este documento no describe nada implementado: dice qué se propone hacer, en qué orden y con qué
criterio de éxito. Lo verificado (V) sale del repositorio o de comprobaciones del 2026-09-20 y cita
su fuente; lo que no se pudo confirmar va marcado (?). Cuando una iniciativa se implemente, se
mueve su estado a V en la misma tarea (regla de `CLAUDE.md`).

Parte de una comparación con mailcow: al reescribir en Go la capa que mailcow resolvía con PHP,
MySQL y SOGo, el sistema ganó multiempresa, permisos, pruebas y funciones de plataforma, pero
quedaron tres puntos por mejorar. Este plan los ataca uno por uno:

* **Eje A.** La calidad de entrega y de antispam sigue dependiendo de los mismos motores de mailcow.
* **Eje B.** El mantenimiento cuesta más: las actualizaciones de mailcow se portan a mano y el
  código Go propio hay que sostenerlo.
* **Eje C.** Faltan funciones de mailcow: calendario y contactos (SOGo), migración de buzones
  (imapsync) y varias opciones de su panel de administración.

## 1. Punto de partida

| Hecho | Estado | Fuente |
|---|---|---|
| Los motores (Postfix, Dovecot, Rspamd 4.1.4, ClamAV 1.4.6, Unbound, Olefy, postfix-tlspol) son los de mailcow, con base fijada en el commit `ca07d8d33318` (versión `2026-09`; el primer port, desde `02552ffefdf0`, se hizo el 2026-09-21) | V | `deploy/mail/UPSTREAM.md`, `deploy/mail/upstream-manifest.tsv` |
| 20 servicios Go, unas 95.000 líneas sin tests y unas 74.000 de tests (1.725 funciones `Test*`), 92 migraciones SQL | V (2026-09-20) | recuento sobre `services/`, `pkg/` y `migrations/` |
| El CI corre `govulncheck` en cada cambio y Dependabot revisa cada semana Go, npm y GitHub Actions. Las imágenes de los motores no tienen Dependabot | V | `.github/workflows/ci.yml`, `.github/dependabot.yml` |
| `make e2e-mail` prueba los motores contra una celda real (156 comprobaciones). En CI corre de noche y ante cambios, sin bloquear | V | `docs/Fase0_Estado.md`, `.github/workflows/mail-engines.yml` |
| Las bases de imagen de los motores son heterogéneas: Debian trixie (postfix, rspamd y, desde el 2026-09-21, postfix-tlspol), Alpine 3.21 (dovecot, olefy), 3.23 (unbound, netfilter, dockerapi, acme, watchdog) y 3.24 (clamav) | V | `deploy/mail/*/Dockerfile` |
| Servidor de producción `89.58.10.80`: MX, SPF, DKIM (`cfm202609`), DMARC y PTR coherentes, puertos 25, 465, 587 y 993 abiertos, certificado válido | V (2026-09-20) | consultas públicas de DNS y TLS |
| Salida real por el puerto 25 desde ese servidor, MTA-STS y TLS-RPT | sin verificar / ausente | idem |
| De 157 ficheros de `deploy/mail/`, 88 son idénticos a mailcow, 62 están modificados (27 solo cambian una etiqueta o un nombre), 1 es nuevo y 6 son propios; el coste de portar se concentra en `postfix/postfix.sh`, `watchdog/watchdog.sh`, `dovecot/docker-entrypoint.sh` y `acme/acme.sh`. La base coincide con la punta de mailcow (0 commits de diferencia) | V (2026-09-20) | `deploy/mail/UPSTREAM.md` |
| El antispam ya trae módulos de Rspamd activos: estadística (bayes), fuzzy, greylisting, ARC, phishing, reputación, límites de tasa y listas RBL | V | `deploy/mail/rspamd/local.d/` |
| La revisión de spam y ham por el usuario ya existe en Dovecot (`report-spam.sieve`, `report-ham.sieve`) | V | `deploy/mail/dovecot/` |

## 2. Reglas que este plan no rompe

Salen de `CLAUDE.md` y limitan lo que se puede proponer:

* Postfix, Dovecot y Rspamd no se parchean por dentro: se configuran y se les sirve lo que necesitan.
* No se vuelve a copiar ningún fichero de mailcow ni del ERP. Un vigilante de cambios puede comparar
  y avisar; lo que interese se reimplementa con criterio propio.
* Nada de PHP ni de MySQL.
* Corporativo y marketing no comparten reputación ni infraestructura de salida.
* Secretos solo en el almacén propio. Ninguna infraestructura nueva sin métricas que la exijan y sin
  ADR en `docs/adr/`.

## 3. Eje A: calidad de correo y antispam

### 3.1 Diagnóstico

Cambiar el lenguaje de la capa de control no mejora lo que se entrega, filtra o rechaza: eso lo
deciden los motores y, sobre todo, la reputación de la IP y del dominio. Con los mismos motores, la
calidad se gana con tres cosas: autenticación completa, reputación construida con cuidado y ajuste
con datos reales. Hoy la autenticación básica está bien; la reputación está sin construir (IP nueva,
sin historial) y el ajuste no puede empezar hasta que haya tráfico.

### 3.2 Objetivos medibles

Todos son objetivos iniciales, a calibrar con la línea base de A1.

| Indicador | Objetivo inicial |
|---|---|
| Correo saliente propio autenticado con SPF y DKIM alineados | 100 % |
| Salida cifrada hacia destinos que ofrecen TLS | 95 % o más |
| Mensajes entregados a Gmail y Outlook en bandeja (no en spam), medido con buzones semilla | 95 % o más tras el calentamiento |
| Correo legítimo retenido o marcado como spam por error (falsos positivos) | menos del 0,1 % |
| Mensajes salientes entregados en menos de 5 minutos | 95 % |

### 3.3 Iniciativas

**A1. Línea base de entregabilidad.** Antes de tocar nada, medir:

* Estado de la IP `89.58.10.80` en las principales listas negras y su historial anterior.
* Alta en Google Postmaster Tools y en Microsoft SNDS y JMRP para el dominio y la IP.
* Envíos de prueba a buzones semilla (Gmail, Outlook, Yahoo, un buzón propio de otro proveedor),
  con lectura de cabeceras de autenticación y carpeta de llegada.
* Verificar la salida por el puerto 25 desde el servidor (`nc -vz gmail-smtp-in.l.google.com 25`).

Tamaño S. Depende de que el proveedor mantenga el puerto 25 saliente abierto.

**A2. Calentamiento de la IP.** La IP no tiene reputación: enviar volumen el primer día hace que los
grandes proveedores la marquen. Propuesta:

* Rampa de volumen por semanas, aplicada con los límites de tasa que `mail-security` ya administra
  (`RateLimitsTab`).
* Empezar por destinatarios conocidos y que respondan (equipos internos, clientes activos).
* El marketing y el transaccional no se calientan aquí: salen por SES con reputación propia.

Tamaño S de configuración, 4 a 6 semanas de calendario.

**A3. Autenticación completa y vigilada.**

* MTA-STS entrante y TLS-RPT: hoy no existen. `postfix-tlspol` solo cubre la salida.
* Procesar los informes DMARC (`rua`): comprobar que existe el buzón `dmarc@core-force.com`, y
  convertir los informes en un panel por dominio.
* Subir DMARC de `p=quarantine` a `p=reject` solo cuando los informes muestren cero fuentes
  legítimas sin alinear durante 30 días.
* BIMI queda fuera hasta que haya marca registrada que lo justifique.

Tamaño M. El procesado de informes es lo más costoso; el resto es DNS y una política.

**A4. Ajuste del antispam con datos reales.**

* Registrar cada cambio de pesos de Rspamd con su motivo y su métrica; ninguno sin dato.
* Usar el aprendizaje que ya existe: lo que el usuario marca como spam o ham entrena el clasificador.
* Medir falsos positivos a partir de la cuarentena y de las liberaciones.
* Valorar la clave DQS de Spamhaus (`SPAMHAUS_DQS_KEY`, ya prevista en el compose): con volumen real,
  los resolvedores públicos limitan las consultas. Comprobar sus condiciones de uso antes de activarla.

Tamaño M, continuo. No arranca hasta que haya tráfico.

**A5. Panel de salud de entrega.** Vista por dominio con rebotes, diferidos, cola, motivos de
rechazo y resultado de autenticación. Depende de que la observabilidad (Prometheus, Grafana y Loki)
esté levantada en el servidor: en el Netcup de 3,8 GB no cabía, en el de 8 GB sí.

Tamaño M. Comparte trabajo con C4 (cola).

**A6. Antivirus.** Mantener las firmas oficiales. Evaluar firmas de terceros solo si aparece un
patrón concreto que las justifique, porque ClamAV ya consume alrededor de 1 GB.

Tamaño S, bajo demanda.

## 4. Eje B: mantenimiento y sostenibilidad

### 4.1 Diagnóstico

Dos costes distintos. El primero es portar a mano cada versión de mailcow sobre unos motores ya
modificados (base de datos, autenticación, SOGo, cron, nombres). El segundo es el código propio: 20
servicios que solo mantiene este proyecto. El primero se puede reducir mucho con método; el segundo
ya está bastante controlado (pruebas, chequeos de acoplamiento, `govulncheck`) y se trata con
gobernanza, no con más código.

### 4.2 Objetivos medibles

| Indicador | Objetivo inicial |
|---|---|
| Parche de seguridad crítico de un motor, en producción | 72 horas o menos desde el aviso |
| Parche importante | 14 días o menos |
| Ficheros de `deploy/mail/` que divergen de mailcow | contarlos y no aumentarlos sin motivo |
| Esfuerzo para portar una versión de mailcow | medir el primer port real y fijar el objetivo después |
| `govulncheck` en verde y ninguna imagen con vulnerabilidad crítica conocida sin plan | siempre |

### 4.3 Iniciativas

**B1. Libro de parches.** Crear `deploy/mail/UPSTREAM.md` con una fila por fichero divergente: ruta
en mailcow, categoría del cambio (PostgreSQL, quitar SOGo, cron, renombre de variables, TLS),
motivo y cómo se rehace. Portar una versión pasa de investigar a aplicar una lista. Tamaño S; es la
base de todo el eje.

**B2. Vigilante de cambios de mailcow.** Un flujo semanal de GitHub Actions, de solo lectura, que
compara el commit fijado con la última versión de mailcow-dockerized limitándose a las rutas que
`deploy/mail/README.md` declara origen, y abre un issue con el resumen y una clasificación:
seguridad, versión de motor, configuración. No copia ningún fichero, así que respeta la regla de
copia única. Tamaño S a M.

**B3. Avisos de seguridad de los motores.** Suscribirse a los anuncios de Postfix, Dovecot, Rspamd,
ClamAV, Unbound y Alpine/Debian, y fijar quién los lee y en cuánto tiempo se actúa (objetivos de
4.2). El despliegue de un motor por commit con `scripts/deploy-mail.sh` ya existe; falta el aviso y
el plazo. Tamaño S.

**B4. Imágenes al día.**

* Armonizar las bases (hoy hay tres versiones de Alpine y dos de Debian) con un criterio y una
  fecha de revisión.
* Añadir a Dependabot el ecosistema `docker` para `deploy/mail/*`.
* Reconstruir las imágenes de los motores cada mes aunque no cambie el código, para recoger parches
  del sistema.
* Escanear las imágenes (por ejemplo con Trivy) en el flujo de motores.

Tamaño M. Hay que decidir el criterio de versiones antes de tocar los Dockerfile.

**B5. Menos divergencia.** Regla para todo cambio nuevo en los motores: si se puede resolver con
`extra.cf`, `custom/`, `local.d/` o variables de entorno, no se edita un fichero copiado. Se añade un
chequeo que cuente los ficheros divergentes y avise si crece. Tamaño S.

**B6. Puerta de regresión para los motores.** `make e2e-mail` pasa a ser condición para desplegar
motores (no para cualquier cambio del repositorio, para no ralentizar el resto). Tamaño S.

**B7. Gobernanza del código Go propio.**

* Cada servicio con responsable y runbook (`docs/Operacion_Despliegue.md` ya cubre la operación).
* Ningún servicio nuevo sin ADR y sin los chequeos existentes (`check-coupling`, contratos de eventos).
* Mantener la proporción actual entre tests y código (unas 0,78 líneas de test por línea de código) y
  no dejar que baje.
* Registro de deuda técnica con fecha, como el de `docs/Fase0_Estado.md`.

Tamaño S, continuo.

**B8. Revisión trimestral de la divergencia.** Cada trimestre, mirar cuántos días costó portar y
cuántos ficheros divergen. Si el coste supera el umbral fijado tras el primer port, decidir por
escrito entre seguir igual, reducir divergencia o cambiar de motor. Tamaño S.

## 5. Eje C: funciones de mailcow que faltan

### 5.1 Criterio

Una función de mailcow se reconstruye solo si un cliente real la pide o si su ausencia bloquea la
adopción. No hay paridad como objetivo: reconstruir todo el panel de mailcow es un coste sin fin.

### 5.2 Inventario

| Función en mailcow | Estado aquí | Prioridad |
|---|---|---|
| Migración de buzones desde otro proveedor (imapsync) | No existe | Alta |
| Respuesta automática (vacaciones) | Backend hecho (`GET/PUT /mailboxes/{id}/vacation`, ver sección 10); falta la pantalla | Alta, coste bajo |
| Gestor de cola de Postfix | `dockerapi` ya sabe ejecutar `mailq`, pero no hay API ni pantalla de la plataforma | Media |
| Calendario y contactos personales (SOGo) | No existe: ni CalDAV, ni CardDAV, ni libreta de direcciones en el webmail | Media, a validar con demanda |
| Sincronización móvil (ActiveSync) | No existe | Descartada (ver C3) |
| Visor de logs y acceso de solo lectura a la interfaz de Rspamd | V (2026-09-21): servicio `observability` sobre Loki (`/platform/logs`) y lectura de `/stat` y `/history` del controller en `mail-security` (`/platform/rspamd`), solo superadmin; `docs/adr/0009` | Hecho |
| Políticas TLS por destino, BCC y mapas de destinatario | V (2026-09-21): existen la API (`/mail-routing/tls-policies`, `/bcc-maps`, `/recipient-maps` en `mail-directory`) y sus pestañas (`web/src/pages/routing/`); el inventario anterior no las había confirmado | Hecho |
| Alias temporales (spam aliases) | Entidad y DTO existen; pantalla sin confirmar (?) | Baja |
| Resto: cuotas, contraseñas de aplicación, alias, límites de tasa, cuarentena, cortafuegos, DKIM, relayhosts | Ya reconstruido en Go y en la web | Hecho |

### 5.3 Iniciativas

**C1. Migración de buzones.** Es la de mayor impacto de adopción: sin ella, un cliente con correo
previo no puede entrar sin perder historial.

* Opción recomendada: envolver `imapsync` en un contenedor efímero lanzado por el `scheduler`, por
  empresa y por buzón. Es una herramienta madura. Antes hay que verificar su licencia.
* Alternativa: implementación propia en Go con una librería IMAP. Da más control y más trabajo.
* Controles obligatorios: credenciales del origen cifradas con `MAIL_ENCRYPTION_KEY` y borradas al
  terminar; contenedor en una red sin acceso a la base ni a Redis y con salida solo hacia el IMAP de
  origen; límite por empresa como derecho de `billing`; progreso y errores visibles; auditoría de
  quién lanzó cada migración.
* Requiere un ADR con la opción elegida.

Tamaño M a L.

**C2. Respuesta automática guiada.** Formulario sobre la extensión `vacation` de Sieve, en el
webmail y en la ficha del buzón, que genera y guarda el script con la API existente. Tamaño S.

**C3. Contactos y calendario.** Se decide por fases y solo si la demanda lo confirma:

1. **Libreta compartida de la empresa** en el webmail, alimentada desde el directorio de correo.
   Coste bajo, valor inmediato.
2. **CardDAV** (contactos) y después **CalDAV** (calendario), con una librería Go de WebDAV sobre la
   base de la empresa, autenticado con `mail-auth` y expuesto por el gateway. Es un servicio nuevo
   con ADR. CardDAV esta implementado (2026-09-21, sin libreria WebDAV: el ADR 0004 explica por que); CalDAV
   sigue pendiente.
3. **ActiveSync: descartado.** Es un protocolo propietario y costoso; el correo móvil se cubre con
   IMAP. Los clientes que necesiten calendario pueden usar el de Google o Microsoft con el buzón
   de esta plataforma.

Tamaño: la fase 1 es S; la fase 2 es L a XL. No se empieza la fase 2 sin decisión explícita del
responsable del producto (dada para CardDAV el 2026-09-21).

**C4. Gestor de cola.** API interna solo para `superadmin` y pantalla: listar, reintentar y borrar
mensajes con su motivo. Se apoya en las tareas que ya tiene `dockerapi`. Ojo: añade una capacidad
privilegiada nueva, así que necesita revisión de seguridad y no debe ampliar la lista de comandos
del API HTTP de doveadm, que se restringe a propósito. Tamaño M. Comparte trabajo con A5.

**C5. Paridad restante.** Pantallas para políticas TLS por destino y mapas BCC y de destinatario;
visor de logs sobre Loki; acceso de solo lectura a la interfaz de Rspamd para el superadmin.
Se hace por demanda, no por calendario. Tamaño S cada una.

## 6. Hoja de ruta

Los plazos son relativos a la fecha de aprobación de este plan.

| Fase | Contenido | Depende de |
|---|---|---|
| 1. Primeros 30 días | A1 línea base, verificación de la salida por el puerto 25, B1 libro de parches, B3 avisos, B6 puerta de regresión, C2 respuesta automática, buzón `dmarc@` | Que el proveedor mantenga el puerto 25 abierto |
| 2. Días 30 a 90 | A2 calentamiento, A3 MTA-STS y TLS-RPT, B2 vigilante, B4 imágenes al día, C1 migración de buzones (ADR y primera versión), observabilidad levantada | Resultados de A1; espacio de RAM del servidor de 8 GB |
| 3. Días 90 a 180 | A4 ajuste con datos reales, A5 panel de entrega, C4 gestor de cola, C3 fase 1 (libreta compartida), B8 primera revisión de divergencia | Tráfico real acumulado |
| Bajo demanda | C3 fase 2, C5, A6 | Petición concreta de un cliente |

## 7. Riesgos

| Riesgo | Efecto | Mitigación |
|---|---|---|
| El proveedor bloquea o limita el puerto 25 saliente | El correo corporativo no puede enviar | Verificar en A1; tener un relayhost como plan B (ver ADR 0001 sobre cómo se guardan sus credenciales) |
| IP con mala reputación heredada | Entrega en spam desde el primer día | Comprobarlo en A1 antes de migrar buzones reales; pedir otra IP si está listada |
| Subir DMARC a `reject` demasiado pronto | Se pierde correo legítimo de terceros que envían en nombre del dominio | Condición de 30 días sin fuentes legítimas sin alinear |
| Un parche de motor rompe la autenticación | Ningún buzón inicia sesión | B6: `make e2e-mail` antes de desplegar; despliegue de un motor a la vez |
| C1 usa credenciales de otros proveedores | Filtración de accesos a cuentas de clientes | Cifrado, borrado al terminar, red aislada, auditoría |
| Construir C3 sin demanda | Meses de trabajo sin uso | Fase 2 solo con decisión explícita |
| Crece la divergencia con mailcow | Cada port cuesta más | B5 y B8 |

## 8. Decisiones que requieren ADR o resolución del responsable

* **C1:** resuelta en `docs/adr/0002-migracion-de-buzones-con-imapsync.md` (imapsync, licencia comprobada). Abierto: OAuth2 de Google y Microsoft y el derecho de `billing`.
* **C3:** si se construye contactos y calendario, y con qué alcance. El responsable del producto debe
  decidir con datos de demanda de clientes.
* **C4:** capacidad privilegiada nueva para operar la cola.
* **B4:** criterio de versiones de las imágenes base.
* **A4:** activar o no la clave DQS de Spamhaus, tras revisar sus condiciones.

## 9. Lo que este plan no hace

* No cambia los motores por otros ni los parchea por dentro.
* No busca paridad completa con el panel de mailcow.
* No toca el correo de marketing ni el transaccional, que salen por SES con su propia reputación.
* No fija fechas de calendario absolutas: dependen del tráfico real y de decisiones pendientes.

## 10. Estado de implementación

Se actualiza en la misma tarea que implemente cada iniciativa.

| Iniciativa | Estado | Qué hay |
|---|---|---|
| B1 Libro de parches | V (2026-09-20) | `deploy/mail/UPSTREAM.md` y `deploy/mail/upstream-manifest.tsv`: los 157 ficheros clasificados, con el motivo y el modo de rehacer cada uno de los 62 modificados |
| B2 Vigilante de cambios de mailcow | V | `ops/upstream/upstream.sh informe`, probado contra datos reales de mailcow y contra un mailcow simulado. El flujo semanal `.github/workflows/upstream-mailcow.yml` corrió por primera vez el 2026-09-22 (lanzado a mano para no esperar al lunes): base `02552ffefdf0` contra `origin/master` de mailcow, 7 commits relevantes, abrió la incidencia #13 con lo pendiente de portar (subida de versiones de Go/Unbound/postfix-tlspol y los ficheros ya divergentes de la sección 5 de `UPSTREAM.md`). Esos 7 commits se portaron a mano el 2026-09-21 (primer port real: postfix-tlspol 1.11.0 sobre trixie, Unbound con versión mínima 1.26.1, `postscreen_access.cidr`, `QUERYwithTLSRPT` en `main.cf.base` y el arreglo de la caché de autenticación de Dovecot), con las imágenes reconstruidas, escaneadas con Trivy (0 críticas, 0 altas) y probadas una a una; la base pasó a `ca07d8d33318`. El despliegue de esos motores exige `make e2e-mail` en verde y va de un motor a la vez (B6) |
| B3 Avisos de seguridad | Parcial | La sección 9 de `UPSTREAM.md` fija de dónde sale cada motor, cómo llega un parche y los plazos. Suscribirse a los avisos es una acción de quien opera el servidor (P) |
| B5 Menos divergencia | V | `ops/scaffold/check-upstream-ledger.sh`, en `validate.sh` (sección 14): editar un fichero idéntico a mailcow sin registrarlo rompe `make checks`; probado con siete mutaciones. El primer port (2026-09-21) redujo la divergencia en un fichero: el parche propio de `x/net` en `postfix-tlspol/Dockerfile` sobraba con la 1.11.0 y se quitó; `main.cf.base` quedó con sus categorías reales (`postgres, nombres, endurecimiento`) |
| B8 Revisión trimestral | V (2026-09-21) | Tabla de `UPSTREAM.md`, sección 10, con la primera fila medida: 7 commits en menos de un día, 7 ficheros, 0 conflictos. Umbral fijado con ese dato: más de 3 días por port, 3 o más conflictos o más de 45 ficheros sustantivos obligan a decidir por escrito (ADR) entre seguir igual, reducir divergencia o cambiar de motor |
| A1 Línea base de entregabilidad | Parcial | `ops/deliverability/verificar-entrega.sh <dominio> --selector <selector>`: MX, SPF (con cuenta de consultas DNS), DKIM (tamaño de clave), DMARC (incluida la autorización de informes a otro dominio), MTA-STS, TLS-RPT, PTR con FCrDNS, saludo SMTP, certificado, puertos y seis listas negras; probado con un `dig` simulado en `ops/scaffold/check-deliverability-tool.sh`. Primera medición, `mentorenergy.uk` el 2026-09-20 con un resolvedor público (`--resolver 1.1.1.1`): 0 fallas y 2 avisos, sin MTA-STS ni TLS-RPT. Con el resolvedor del sistema salió además un aviso de DMARC que era la caché local de un cambio ya hecho en producción (informes a `reportes@dmarc.core-force.com`, con la autorización comodín publicada); de ahí la opción `--resolver`. Falta lo que no se ve desde fuera: la salida por el puerto 25, Postmaster Tools, SNDS y los buzones semilla (P) |
| B4 Imágenes al día | V | Escaneo mensual con Trivy: `ops/security/escanear-motores.sh` y `.github/workflows/imagenes-motores.yml`, con su prueba en `ops/scaffold/check-motor-scan.sh`. Decidido no usar Dependabot para estas imágenes (`UPSTREAM.md`, sección 9). Primera pasada real sobre las 11 imágenes (2026-09-20): 1 crítica y 35 altas con arreglo, corregidas y confirmadas con un reescaneo (0 y 0). Primera ejecución del flujo en GitHub, 2026-09-22 (lanzada a mano): 0 críticas y 0 altas con arreglo publicado, incidencia cerrada sin nada pendiente. Pendiente: armonizar las bases (criterio y fecha de revisión) y la reconstrucción mensual, que hace quien opera el servidor (P) |
| C2 Respuesta automática | Hecho | V (2026-09-21): `mail.vacation_replies` y `v_sieve_vacation` (`09_vacation.sql`), API de administración e interna en `mail-directory`, tercera ranura `sieve_after3` en Dovecot, `make e2e-mail` con Dovecot real (fuera de la ventana no contesta, dentro contesta, cabecera de respuesta automática, texto literal); ruta `GET`/`PUT /api/v1/webmail/vacation` en `webmail` (el buzón sale de la sesión); pantallas en la ficha del buzón (pestaña) y en el webmail (`/webmail/settings`), con los topes que sirve el propio API y pruebas de vitest. Límites conocidos: solo contesta a mensajes dirigidos a la dirección del buzón (no a sus alias) y las fechas se comparan en UTC. Al desplegar: migración de la celda, luego `mail-directory`, luego Dovecot y por último `webmail` |
| C3 fase 1 Libreta compartida | Hecho | V (2026-09-21): `GET /internal/mail-directory/directory?username=&q=&limit=` en `mail-directory` (buzones activos de la empresa del buzon que pregunta, solo direccion y nombre visible, tope 20 por defecto y 50 como máximo, búsqueda por texto sin comodines) y `GET /api/v1/webmail/address-book?q=&limit=` en `webmail` (la empresa sale de la sesión, nunca de la petición); selector "Elegir de la empresa" en la redacción. Probado con Postgres real (aislamiento entre empresas, buzones apagados fuera; mutación comprobada) y `make e2e-mail`. La fase 2 (CardDAV y CalDAV) se construyo despues, con la decisión explícita del producto (fila siguiente). No hay dato nuevo en la base: sale del directorio que ya existe, así que no hay migración |
| B7 Gobernanza del código Go | Parcial | V (2026-09-21): `ops/scaffold/check-test-ratio.sh`, en `validate.sh` (sección 17) y por tanto en `make checks`: la proporción de pruebas por servicio no baja de su suelo (`test-ratio-floors.txt`, 0,79 en total al medirlo) y un servicio nuevo nace con al menos 0,50; probado con mutaciones. Ya existían `check-coupling` y `check-event-contracts` para los servicios nuevos. Hallazgo: `audit` (la cadena de hashes) estaba en 0,23, el más bajo de todos. V (2026-09-21): subido a 1,46 con pruebas contra Postgres real (cadena, manipulación, concurrencia, aislamiento; probadas con mutaciones) y con los defectos que salieron corregidos; V (2026-09-21): la cadena de hash de `audit` pasa a la versión 2 (HMAC con clave, longitud por campo, `user_agent`, cadena de eventos de seguridad y anclas de la cabeza; ADR 0006), con suelo de `audit` en 1,49 y probada con mutaciones; lo que queda abierto (poner `AUDIT_HASH_KEY` y la exportación del ancla fuera del servidor) está en `Fase0_Estado.md`. V (2026-09-21, sin desplegar): `audit` recibe los eventos del bus por consumidores durables de JetStream (antes perdía lo publicado con el servicio caído) y verifica la cadena en segundo plano (`POST /audit/integrity/runs`, `audit.integrity_runs`, reanudable, cancelable, incremental, con barrido opcional y alertas), con pruebas contra NATS y Postgres reales y mutaciones; ADR 0006, secciones 6 y 7. V (2026-09-21, sin desplegar): el ancla externa por correo (ADR 0006, sección 8): con `AUDIT_ANCHOR_RUA` puesta, `audit` envía cada `AUDIT_ANCHOR_REPORT_INTERVAL` el informe firmado con la última ancla de cada cadena de cada empresa como correo de la plataforma, y de inmediato el de una cadena rota; `ops/security/verificar-ancla.sh` (subcomando `audit verificar-ancla`) comprueba la firma fuera del servidor y coteja con la cadena por la base; métrica `audit_anchor_reports_total{result}` y alerta `AnclaDeAuditoriaSinEnviar`; probado contra Postgres real con un `transactional` simulado y mutaciones del correo y de la cadena; suelo de `audit` en 1,53. Queda la copia en un objeto con retención inmutable fuera del host (sin destino ni credenciales) y, del lado del operador (P), configurar la dirección externa y conservar los correos fuera. Falta lo que es decisión de personas (P): un responsable por servicio y su runbook |
| A2 Calentamiento de la IP | Parcial | `docs/Calentamiento_de_IP.md`: rampa por etapas con el límite de envío por dominio que `mail-security` ya administra (sin código nuevo), condiciones para subir de etapa y de retroceso, y dónde mirar cada señal. Sin ejecutar (P): depende de confirmar la salida por el puerto 25 y de registrar Postmaster Tools y SNDS |
| C4 Gestor de cola | Hecho (P: activarlo en produccion) | V (2026-09-21): agente propio `deploy/mail/postfix/queue-agent` (Go, biblioteca estandar, dentro del contenedor de Postfix, HTTPS y clave) en lugar de ampliar `dockerapi`; API `GET|POST|DELETE /api/v1/mail-security/queue` solo para el superadmin con registro de quien actua; pantalla `/platform/mail-queue`. Probado con Postfix real en `make e2e-mail` (listar, retener, liberar, reintentar, vaciar, borrar, 403 a un administrador de empresa y las negativas del agente) y con mutaciones. La revision de seguridad que pedia el plan queda en `deploy/mail/README.md` (Gestor de la cola): superficie, privilegios y por que no `dockerapi`. Para activarlo en produccion: migracion 033 del registro, `QUEUE_AGENT_API_KEY` en el almacen y recrear `mail-security` y `postfix-mail` (P) |
| B6 Puerta de regresion | Hecho (P: `gh auth login` en el puesto de despliegue) | V (2026-09-21): `despliegue_comprobar_regresion` en `scripts/lib/despliegue.sh`, llamada por `scripts/deploy-mail.sh` antes de construir: exige `make e2e-mail` en verde (flujo `mail-engines.yml`) para HEAD o para el ultimo verde anterior sin cambios en los `paths` que el flujo vigila; una ejecucion fallida o ninguna la detiene. `MAIL_DEPLOY_REGRESION=avisar\|omitir` para casos justificados. Probada con `gh` falso y tres mutaciones en `ops/scaffold/check-deploy-mail.sh`. Necesita `gh` autenticado en quien despliega |
| C1 Migracion de buzones | Primera version implementada (2026-09-21); sin `make e2e-mail` ni proveedor real | `docs/adr/0002-migracion-de-buzones-con-imapsync.md` (estado, decisiones y pruebas). Hecho: servicio `mail-migration` (plano de empresa: trabajos, credencial cifrada y borrada al terminar, reclamo con `SKIP LOCKED` y lease, limite por empresa, auditoria por outbox, permisos `migration/jobs/*`, validacion anti-SSRF); ejecutor `mail-migration-runner` (imapsync 2.314 de Alpine, porque Debian y Ubuntu no lo empaquetan; sin acceso a base ni Redis; red propia; usuario maestro de Dovecot propio; conecta a la IP ya validada con el certificado verificado contra el nombre; ClamAV por `--pipemess` antes de copiar cada mensaje, cerrado en el ejecutor); pestana Migracion en la ficha del buzon. Probado: unitarias, integracion contra Postgres, imapsync real con Dovecot y clamd en un docker efimero. Escrita y SIN ejecutar: la seccion de migracion de `ops/e2e/mail.sh` (solo `bash -n`). Hecho (2026-09-21): al borrar un buzon, consumidor de `mail.mailbox.deleted` que borra sus trabajos (con credencial, usuario y servidor de origen) por id de buzon y solo en la base de su empresa, y anuncia la cancelacion de los activos a la auditoria en la misma transaccion (integracion con mutacion). Hecho (2026-09-21): credencial de destino por trabajo (V, opt-in con `MAIL_MIGRATION_JOB_CREDENTIALS`): cada trabajo reclamado trae una credencial de 256 bits cuyo hash vive en la fila del trabajo (`04_destination_credential.sql`) y que Dovecot (passdb Lua `migration-verify.lua`, sin cache por sesion) valida contra `mail-auth`, que exige la red del ejecutor y pregunta a `mail-migration`; abre solo el buzon del trabajo mientras esta en curso y se revoca al cancelar, cerrar, vencer el lease y borrar el buzon; probada con Dovecot 2.3.21 real en un contenedor efimero y con Postgres real (mutaciones) y, escrita y SIN ejecutar, en la seccion nueva de `ops/e2e/mail.sh`. Hecho (2026-09-21): barrido de conciliacion de buzones borrados (`pkg/mailreconcile`, ver C3), que retira los trabajos huerfanos. Falta (P): retirar el maestro compartido en produccion siguiendo el orden de `deploy/mail/README.md` (hasta entonces el riesgo residual sigue abierto), ejecutar `make e2e-mail`, OAuth2 de Google y Microsoft (decision de producto), el limite como derecho de `billing`, TLS entre `mail-migration` y el ejecutor, la regla `DOCKER-USER` del host, varias celdas (destino Dovecot fijo), IPv6 probado con imapsync real, y desplegar los motores antes que la plataforma (red `mail-migration`) |
| A3 MTA-STS y TLS-RPT | Parcial (codigo hecho; el borde es P) | `docs/adr/0003-mta-sts-y-tls-rpt-entrantes.md`. V (2026-09-21): `mail.mta_sts_policies` (`10_mta_sts.sql`, RLS, `mail_engine` sin acceso) y API en `mail-directory` (`GET /api/v1/mail-domains/mta-sts` y `GET`/`PUT .../{dominio}`, permisos `domains/mta_sts/{read,update}` sembrados en `034`): se activa en `testing`, se entra y se sale de `enforce` por `testing`, y `enforce` exige el dominio verificado y activo y todos sus MX publicados iguales a `MAIL_MX_HOSTNAME`; ruta publica `GET /public/mail-directory/mta-sts/{cell}/{dominio}` con el cuerpo RFC 8461 (declarada en `routes.json`); `domain-service` pide los TXT recomendados `_mta-sts` (con el id de la politica, que lee de `mail-directory`) y `_smtp._tls` (solo con `MAIL_TLSRPT_RUA`) y los verifica y publica en el proveedor DNS; panel de MTA-STS en la ficha del dominio con confirmacion para `enforce`. Probado con unitarias, integracion contra Postgres (RLS, CHECK y la mutacion de cada uno) y pruebas de web; las comprobaciones de punta a punta estan escritas en `ops/e2e/mail.sh` y sin ejecutar (P). Sigue pendiente del borde (P): que `mta-sts.<dominio>` llegue a esa ruta con un certificado valido (CNAME del cliente, `acme` con una vista de los dominios con politica, y `selfhosted/edge` atendiendo el puerto 80 y el SNI de esos nombres), sin lo cual la politica no protege a nadie; y con varias celdas, que el borde sepa la celda de cada dominio. Sin hacer: comprobar el certificado al pasar a `enforce`, el buzon de `MAIL_TLSRPT_RUA` y el panel de informes DMARC y TLS-RPT |
| A5 Panel de salud de entrega | Parcial | V (2026-09-21): la cola de Postfix se vigila (`QueueMonitor` de `mail-security` sobre el agente de la cola): metricas `mail_security_postfix_queue_*` y las alertas `ColaDePostfixAtascada` y `GestorDeColaSinRespuesta`, con pruebas de `promtool` y comprobacion en `make e2e-mail`. Panel de Grafana `entrega-mail` (`ops/observability/grafana/dashboards/entrega.json`, con `ops/scaffold/check-dashboards.sh` en `make checks`): cola por estado, el mensaje mas antiguo, falsos positivos de la semana, cuarentena por desenlace y, desde el registro de Postfix en Loki, entregas, diferidos, rebotes, rechazos por causa y los motivos. Queda por dominio y por proveedor (el registro de Postfix no lleva la empresa) y el resultado de autenticacion (Rspamd); y verlo funcionando depende de que la observabilidad este levantada en el servidor, donde no lo esta hoy (P) |
| A4 Ajuste del antispam | Parcial | V (2026-09-21): `mail_security_quarantine_messages_total{outcome}` (retenidos, liberados, descartados y entrenados como spam) con pruebas unitarias, para medir falsos positivos, y `docs/Ajuste_Antispam.md` con la regla (ningun peso sin dato), las consultas, el registro de cambios y las condiciones previas de DQS. No arranca el ajuste hasta que haya trafico real (P). Hecho tambien el aprendizaje contrario: "Liberar y marcar como legitimo" (`release-ham`, `learned_ham`), con permisos de liberar y de entrenar |
| C3 fase 2 CardDAV y CalDAV | Parcial: CardDAV y CalDAV (eventos) implementados (2026-09-21); sin cliente real ni `make e2e-mail` | `docs/adr/0004-contactos-y-calendario-carddav-caldav.md` (estado, decisiones y pruebas). Hecho: servicio `mail-dav` de empresa (`mail_dav`, RLS por buzon) con `PROPFIND`, `REPORT` (`addressbook-query`, `addressbook-multiget`, `sync-collection`), `GET`, `PUT` con `If-Match`, `DELETE` y `MKCOL`, protocolo escrito a mano (go-webdav se descarto: su servidor no implementa `sync-collection` ni `getctag` y no acota la entrada); Basic contra `mail-auth` con el servicio `dav` y el flag `dav_access` del buzon y de la contrasena de aplicacion; prefijo `self_authenticated` y `/.well-known/carddav` en el gateway; datos de conexion en la ficha del buzon; limites por buzon; pruebas unitarias, de integracion contra Postgres real (aislamiento con mutacion) y de gateway. Hecho (2026-09-21, CalDAV): calendarios y eventos `VEVENT` por buzon en `mail_dav` (migracion `03_caldav.sql`: `calendars`, `events`, `calendar_changes`, con RLS por buzon), con `PROPFIND` (`calendar-home-set`, `getctag`, `sync-token`), `REPORT` (`calendar-query` con `time-range`, `comp-filter` y `prop-filter`; `calendar-multiget`; `sync-collection`), `GET`, `PUT` con `If-Match`, `DELETE`, `MKCALENDAR` y `MKCOL` de calendario, `/.well-known/caldav` y `MKCALENDAR` en el gateway; iCalendar validado y acotado con parser propio (sin libreria: se evaluaron `go-ical`, `rrule-go` y `golang-ical`, todas con licencia compatible, y se descartaron por el contrato de presupuesto y de "devolver lo que no se puede decidir"), recurrencias guardadas sin expandir y expansion acotada solo para decidir un `time-range`, `VTIMEZONE` del cliente o base IANA, filtros no soportados rechazados con `supported-filter`, limites por buzon y de trabajo de expansion, borrado por buzon borrado extendido a calendarios; pruebas unitarias, de protocolo (cuerpos de DAVx5, iOS y Thunderbird escritos a mano), de integracion con mutaciones y la seccion CalDAV de `ops/e2e/mail.sh` (escrita, sin ejecutar); la ficha del buzon muestra la URL para contactos y calendarios. Hecho (2026-09-21): consumidor de `mail.mailbox.deleted` que borra las libretas y contactos del buzon por su id, en la base de su empresa y sin cruzar empresas ni tocar un buzon recreado con el mismo nombre (unitarias, integracion con mutacion; `mail-migration` hace lo mismo con sus trabajos, C1). Falta: probar con DAVx5, Thunderbird e iOS, ejecutar las secciones nuevas de `make e2e-mail` (CardDAV y CalDAV), tareas (`VTODO`), invitaciones (iTIP/iMIP), alarmas, `free-busy-query`, comparticion, la libreta "Directorio de la empresa", limites como derechos del plan (P). Hecho (2026-09-21): barrido de conciliacion de buzones borrados (`pkg/mailreconcile` compartido con `mail-migration`, cerrojo de lider, gracia, tope por empresa y pasada, `MAIL_DAV_RECONCILE_*`), con la API interna `POST /internal/mail-directory/mailboxes/existence` y la funcion `mail_dav.stale_mailbox_ids` (`04_reconcile.sql`); probado con Postgres real y mutaciones |
| C5 Paridad restante | Hecho (P: desplegar y ejecutar los e2e) | Las pantallas de politicas TLS por destino, BCC y mapas de destinatario ya existian (ver el inventario). V (2026-09-21, `docs/adr/0009-visor-de-registros-y-lectura-del-antispam.md`): visor de registros sobre Loki en el servicio nuevo `observability` (plano de control, sin base ni bus; lista blanca fija de servicios, un unico filtro de linea literal escapado con la gramatica de LogQL, ventana de 24 h y 500 lineas como mucho, 30 consultas por minuto y usuario, registro de cada consulta sin el texto buscado, 503 `NOT_CONFIGURED` sin `LOKI_URL`) y lectura de solo lectura del controller de Rspamd en `mail-security` (`/rspamd/stats` y `/rspamd/history`, sobre y veredicto sin cuerpos ni opciones de simbolos, contrasena solo en la cabecera; ninguna ruta de escritura), las dos solo para el superadmin con permisos de plataforma (`036`, `037`) y pantallas `/platform/logs` y `/platform/rspamd`. Alternativas descartadas en el ADR: proxy a la interfaz de Rspamd, Grafana embebido y LogQL libre. Probado con Loki y controller falsos, casos de inyeccion y vitest; las secciones de `make e2e` (503 y 403) y `make e2e-mail` (estadisticas e historial reales con la contrasena del controller generada en la prueba) estan escritas y sin ejecutar (P). Para activarlo en produccion: migraciones `036` y `037`, `LOKI_URL` (la fija el perfil), la contrasena del controller en Rspamd y en `mail-security`, y recrear `gateway`, `observability`, `mail-security` y `rspamd-mail` (P) |
| A6 Antivirus | Hecho (nada que hacer hoy) | Las firmas oficiales se actualizan con `freshclam` desde `deploy/mail/clamav/clamd.sh` (al arrancar y de forma periodica). Las firmas de terceros solo se evaluaran si aparece un patron concreto que las justifique, como dice el plan |

