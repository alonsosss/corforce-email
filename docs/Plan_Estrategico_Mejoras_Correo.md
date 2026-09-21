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
| Los motores (Postfix, Dovecot, Rspamd 4.1.4, ClamAV 1.4.6, Unbound, Olefy, postfix-tlspol) son los de mailcow, con base fijada en el commit `02552ffefdf0` | V | `deploy/mail/README.md`, `deploy/mail/rspamd/Dockerfile` |
| 20 servicios Go, unas 95.000 líneas sin tests y unas 74.000 de tests (1.725 funciones `Test*`), 92 migraciones SQL | V (2026-09-20) | recuento sobre `services/`, `pkg/` y `migrations/` |
| El CI corre `govulncheck` en cada cambio y Dependabot revisa cada semana Go, npm y GitHub Actions. Las imágenes de los motores no tienen Dependabot | V | `.github/workflows/ci.yml`, `.github/dependabot.yml` |
| `make e2e-mail` prueba los motores contra una celda real (156 comprobaciones). En CI corre de noche y ante cambios, sin bloquear | V | `docs/Fase0_Estado.md`, `.github/workflows/mail-engines.yml` |
| Las bases de imagen de los motores son heterogéneas: Debian trixie (postfix, rspamd), Debian bookworm (postfix-tlspol), Alpine 3.21 (dovecot, olefy), 3.23 (unbound, netfilter, dockerapi, acme, watchdog) y 3.24 (clamav) | V | `deploy/mail/*/Dockerfile` |
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
| Visor de logs y acceso de solo lectura a la interfaz de Rspamd | No existe (?) | Baja |
| Políticas TLS por destino, BCC y mapas de destinatario | Las tablas y entidades existen y los motores las leen; la API y la pantalla no las confirmé (?) | Baja |
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
   con ADR.
3. **ActiveSync: descartado.** Es un protocolo propietario y costoso; el correo móvil se cubre con
   IMAP. Los clientes que necesiten calendario pueden usar el de Google o Microsoft con el buzón
   de esta plataforma.

Tamaño: la fase 1 es S; la fase 2 es L a XL. No se empieza la fase 2 sin decisión explícita del
responsable del producto.

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

* **C1:** herramienta (imapsync o implementación propia) y verificación de su licencia.
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
| B2 Vigilante de cambios de mailcow | V la herramienta, P la ejecución | `ops/upstream/upstream.sh informe`, probado contra datos reales de mailcow y contra un mailcow simulado. El flujo semanal `.github/workflows/upstream-mailcow.yml` está escrito y su YAML es válido, pero aún no ha corrido en GitHub |
| B3 Avisos de seguridad | Parcial | La sección 9 de `UPSTREAM.md` fija de dónde sale cada motor, cómo llega un parche y los plazos. Suscribirse a los avisos es una acción de quien opera el servidor (P) |
| B5 Menos divergencia | V | `ops/scaffold/check-upstream-ledger.sh`, en `validate.sh` (sección 14): editar un fichero idéntico a mailcow sin registrarlo rompe `make checks`; probado con siete mutaciones |
| B8 Revisión trimestral | Parcial | Primera fila de la tabla de `UPSTREAM.md`, sección 10. El umbral se fija tras el primer port real |
| A1 Línea base de entregabilidad | Parcial | `ops/deliverability/verificar-entrega.sh <dominio> --selector <selector>`: MX, SPF (con cuenta de consultas DNS), DKIM (tamaño de clave), DMARC (incluida la autorización de informes a otro dominio), MTA-STS, TLS-RPT, PTR con FCrDNS, saludo SMTP, certificado, puertos y seis listas negras; probado con un `dig` simulado en `ops/scaffold/check-deliverability-tool.sh`. Primera medición, `mentorenergy.uk` el 2026-09-20 con un resolvedor público (`--resolver 1.1.1.1`): 0 fallas y 2 avisos, sin MTA-STS ni TLS-RPT. Con el resolvedor del sistema salió además un aviso de DMARC que era la caché local de un cambio ya hecho en producción (informes a `reportes@dmarc.core-force.com`, con la autorización comodín publicada); de ahí la opción `--resolver`. Falta lo que no se ve desde fuera: la salida por el puerto 25, Postmaster Tools, SNDS y los buzones semilla (P) |
| B4 Imágenes al día | Parcial | Escaneo mensual con Trivy: `ops/security/escanear-motores.sh` y `.github/workflows/imagenes-motores.yml` (el flujo aún no ha corrido en GitHub), con su prueba en `ops/scaffold/check-motor-scan.sh`. Decidido no usar Dependabot para estas imágenes (`UPSTREAM.md`, sección 9). Primera pasada real sobre las 11 imágenes: 1 crítica y 35 altas con arreglo (la librería estándar de Go dentro de `gosu`, `setuptools` y el `msgpack` que trae `pip` en los tres motores de Python, y `golang.org/x/net` en `postfix-tlspol`, la única alcanzable en la práctica); corregidas en `deploy/mail/{dovecot,olefy,dockerapi,netfilter,postfix-tlspol}/Dockerfile` y confirmadas con un reescaneo: 0 y 0. Pendiente: armonizar las bases (criterio y fecha de revisión) y la reconstrucción mensual, que hace quien opera el servidor (P) |
| C2 Respuesta automática | Hecho | V (2026-09-21): `mail.vacation_replies` y `v_sieve_vacation` (`09_vacation.sql`), API de administración e interna en `mail-directory`, tercera ranura `sieve_after3` en Dovecot, `make e2e-mail` con Dovecot real (fuera de la ventana no contesta, dentro contesta, cabecera de respuesta automática, texto literal); ruta `GET`/`PUT /api/v1/webmail/vacation` en `webmail` (el buzón sale de la sesión); pantallas en la ficha del buzón (pestaña) y en el webmail (`/webmail/settings`), con los topes que sirve el propio API y pruebas de vitest. Límites conocidos: solo contesta a mensajes dirigidos a la dirección del buzón (no a sus alias) y las fechas se comparan en UTC. Al desplegar: migración de la celda, luego `mail-directory`, luego Dovecot y por último `webmail` |
| C3 fase 1 Libreta compartida | Hecho | V (2026-09-21): `GET /internal/mail-directory/directory?username=&q=&limit=` en `mail-directory` (buzones activos de la empresa del buzon que pregunta, solo direccion y nombre visible, tope 20 por defecto y 50 como máximo, búsqueda por texto sin comodines) y `GET /api/v1/webmail/address-book?q=&limit=` en `webmail` (la empresa sale de la sesión, nunca de la petición); selector "Elegir de la empresa" en la redacción. Probado con Postgres real (aislamiento entre empresas, buzones apagados fuera; mutación comprobada) y `make e2e-mail`. La fase 2 (CardDAV y CalDAV) sigue sin empezar y requiere decisión explícita del producto. No hay dato nuevo en la base: sale del directorio que ya existe, así que no hay migración |
| B7 Gobernanza del código Go | Parcial | V (2026-09-21): `ops/scaffold/check-test-ratio.sh`, en `validate.sh` (sección 17) y por tanto en `make checks`: la proporción de pruebas por servicio no baja de su suelo (`test-ratio-floors.txt`, 0,79 en total al medirlo) y un servicio nuevo nace con al menos 0,50; probado con mutaciones. Ya existían `check-coupling` y `check-event-contracts` para los servicios nuevos. Hallazgo: `audit` (la cadena de hashes) está en 0,23, el más bajo de todos; subirlo es deuda anotada en `Fase0_Estado.md`. Falta lo que es decisión de personas (P): un responsable por servicio y su runbook |
| B6, A2 a A6, C1, C3 fase 2, C4, C5 | P | Sin implementar |

