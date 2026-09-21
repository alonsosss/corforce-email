# ADR 0003: MTA-STS y TLS-RPT para los dominios de las empresas

## Estado

Parcialmente implementado (2026-09-21). Cierra el diseño de `Plan_Estrategico_Mejoras_Correo.md`, A3, en lo que toca
a MTA-STS y TLS-RPT. Hecho: la parte de código que no cambia la exposición (tabla, API con la verificación previa a
`enforce`, manejador de la política, registros `_mta-sts` y `_smtp._tls` en `domain-service`, panel en la ficha del
dominio; "Implementación" abajo). Pendiente: todo lo que depende del proxy de borde y de los certificados, que es de
quien opera el servidor, y el procesado de los informes.

## Contexto

`postfix-tlspol` cubre la salida: Postfix consulta MTA-STS y DANE de los dominios a los que envía. Lo entrante
no existe: ningún dominio de una empresa publica una política MTA-STS ni un registro TLS-RPT, de modo que un
atacante en la ruta puede degradar a texto plano el correo que otros servidores nos envían.

MTA-STS (RFC 8461) exige, por cada dominio de cliente `d`:

1. Un TXT `_mta-sts.d` con `v=STSv1; id=<versión>`.
2. Un servidor HTTPS en `mta-sts.d` que sirva `https://mta-sts.d/.well-known/mta-sts.txt` con un certificado
   **válido para ese nombre** (versión, modo, lista de MX, `max_age`).
3. Opcionalmente un TXT `_smtp._tls.d` con `v=TLSRPTv1; rua=mailto:...` para recibir informes de fallos de TLS
   (RFC 8460).

Hoy `acme.sh` ya intenta pedir certificados `mta-sts.<dominio>` para los dominios de una tabla que no existe
(`deploy/mail/README.md`, Pendientes), y el proxy de borde (`selfhosted/edge`) atiende solo el nombre de
`PUBLIC_BASE_URL`, detrás de Cloudflare.

## Decisión

1. **Estado por dominio en la base de la celda**, no en el registro: `mail.mta_sts_policies(tenant_id, domain,
   mode, max_age, id, updated_at)` en el servicio dueño del directorio (`mail-directory`), con `mode` en
   `none`, `testing` o `enforce` y **`testing` como valor inicial obligatorio**. Pasar a `enforce` es una
   acción explícita del `tenant_admin` (con paso extra de confirmación) y solo si la verificación de DNS y
   certificado del propio dominio lo permite: una política `enforce` con un MX o un certificado que no casan
   hace que otros servidores **dejen de entregar** correo.
2. **La política se sirve dinámicamente**, no con ficheros: un manejador público sin sesión en
   `mail-directory` (`GET /public/mail-directory/mta-sts/{dominio}`) que devuelve el cuerpo a partir de la
   fila y de los MX de la plataforma (`MAIL_MX_HOSTNAME`), con el gateway como única entrada y la ruta
   declarada en `routes.json`. El `id` cambia con cada modificación.
3. **Certificados y borde** (decisión de operación, no de código): `mta-sts.<dominio>` es un nombre por
   cliente, ajeno a la zona de la plataforma. Requiere que el cliente publique un CNAME `mta-sts` hacia el
   borde de la plataforma, que `acme` obtenga un certificado por cada uno (lo hace por HTTP-01, por lo que el
   borde debe atender el puerto 80 de esos nombres sin Cloudflare por medio) y que el borde presente el
   certificado por SNI. Es la parte que cambia `selfhosted/edge` y su modelo de exposición: se trata como
   cambio de infraestructura aparte, con decisión de quien opera el servidor.
4. **`domain-service` publica los registros** (`_mta-sts`, `mta-sts` y `_smtp._tls`) con los mismos
   mecanismos de DNS que SPF, DKIM y DMARC (manual o proveedor conectado) y los verifica; son
   recomendados, no requeridos, como DMARC. El TXT de `_mta-sts` lleva el `id` de la política.
5. **TLS-RPT** necesita un buzón que reciba los informes (`MAIL_TLSRPT_RUA`, como `MAIL_DMARC_RUA`); su
   procesado y el de los informes DMARC (`rua`) es el panel de A3 y es el trabajo más grande: se hace después.

## Alternativas descartadas

* **Ficheros estáticos por dominio en el borde**: obligan a regenerarlos y recargar nginx con cada cambio y
  no dejan hacer el paso `testing` a `enforce` con verificación.
* **Servirlo desde el proxy de borde por SNI sin acme**: no hay certificado válido para nombres de clientes
  sin que alguien lo emita.
* **Activar `enforce` por defecto**: rompería la entrega ante el primer fallo de certificado.

## Consecuencias y qué falta para empezar

* Cambia la superficie pública (un puerto 80/443 con nombres de clientes) y el modelo del borde: no se
  empieza sin aprobación de quien opera el servidor.
* Se puede adelantar la parte que no cambia la exposición: la tabla, la API de administración con la
  verificación previa a `enforce`, los registros `_mta-sts` y `_smtp._tls` en `domain-service`, y el
  manejador de la política. Sin el borde y los certificados no protege a nadie, pero deja todo listo y probado.
* Sin cambios en la salida: `postfix-tlspol` sigue igual.

## Implementación (2026-09-21)

Lo que se hizo y las decisiones que el diseño dejaba abiertas.

1. **Tabla y modos.** `mail.mta_sts_policies` (`migrations/cell/canonical/mail-directory/10_mta_sts.sql`): `domain`
   único, `mode` (`none`, `testing`, `enforce`; `testing` por defecto), `max_age` (1 día en `testing`, 7 en `enforce`),
   `policy_id` y RLS por empresa; sin fila es `none`. El id no se llama `id` como en el borrador porque `id` ya es la
   clave de la fila; `policy_id` es el del TXT.
2. **Transiciones.** Se entra a `enforce` y se sale de él por `testing` (`domain.MTASTSTransition`). Un remitente que
   guardó `enforce` lo sigue aplicando hasta que vence su caché, así que apagar de golpe una política endurecida
   dejaría sin efecto lo que ya se anunció; `testing` no bloquea entregas y sustituye a la anterior en cuanto cambia su
   id. Repetir el modo actual no cambia nada ni renueva el id.
3. **Verificación previa a `enforce`** (`app.SetMTASTSMode`): el dominio existe en el directorio y está activo (lo
   activa `domain-service` al verificarlo) y **todos** sus MX publicados, consultados en el momento, son
   `MAIL_MX_HOSTNAME`: con `enforce` los remitentes solo entregan a los MX de la política, y un MX propio que no esté
   en ella se quedaría sin correo. `mail-directory` hace la consulta (adaptador `dns`, mismo `MAIL_DNS_RESOLVER` que
   `domain-service`); un fallo del DNS es 503 `DNS_UNAVAILABLE`, no un MX que no cuadra. **No** comprueba todavía el
   certificado de `mta-sts.<dominio>` ni que la política se pueda descargar: depende del borde. Sin descarga posible los
   remitentes no aplican política alguna, así que un `enforce` sin borde no bloquea nada, pero tampoco protege.
4. **De dónde sale el id que anuncia `domain-service`: lo lee de `mail-directory`.** Se descartó que `domain-service`
   lo calcule de forma determinista porque necesitaría el modo y el `max_age`, que viven en la celda; duplicar ese estado
   en la base de la empresa habría exigido eventos, un consumidor y una migración más, y ya hay una llamada
   servicio a servicio a `mail-directory` con la celda de la empresa. La llamada es `GET
   /internal/mail-directory/mta-sts/{dominio}` (empresa en `X-Tenant-ID`, la misma respuesta que la de la interfaz), solo
   para dominios que reciben por la celda. Un fallo o un 404 (dominio que la celda no tiene, instancia que no sirve la
   ruta) deja fuera únicamente ese TXT, que no es requerido, y se registra. Sin claves foráneas entre esquemas y sin más
   acoplamiento que el que ya existía en ese sentido.
5. **Permisos: módulo `domains`, no uno nuevo.** `domains/mta_sts/{read,update}` (`034_mail_directory_mta_sts_permissions.sql`,
   alcance `tenant`). El prefijo `mail-domains` del gateway ya gatea el módulo `domains`; un módulo `mail_directory`
   nuevo habría exigido otra entrada en el menú y otro prefijo por una sola pantalla. La API cuelga de
   `/api/v1/mail-domains/mta-sts`.
6. **Ruta pública con `{cell}`.** `GET /public/mail-directory/mta-sts/{cell}/{dominio}`, no `.../{dominio}` a secas:
   `mail-directory` es un servicio de celda y el gateway se niega a arrancar con una ruta pública de un servicio de celda
   sin el segmento `{cell}` (`validatePublicCell`). El segmento solo enruta, sin firmar. Con una sola celda da igual lo que
   lleve; con varias, el borde tiene que conocer la celda de cada dominio. El manejador no pide sesión ni empresa: lee con
   el rol de servicio y solo sirve una política de un dominio activo y de la misma empresa que la escribió.
7. **`none` no se sirve** (404), como pide el diseño; junto con la regla 2 (no se llega a `none` desde `enforce`) evita
   dejar a un remitente con un `enforce` en caché y sin política que lo reemplace. RFC 8461 8.3 describe la retirada
   servida como `mode: none`; si algún día se quiere, es cambiar `PublishedMTASTS`.
8. **Registros.** `domain-service` añade `RecordMTASTS` (`mta_sts`) y `RecordTLSRPT` (`tls_rpt`), ambos `Required:
   false`, solo en dominios que reciben por la celda: `_mta-sts.<dominio>` = `v=STSv1; id=<policy_id>` (solo con la
   política activa) y `_smtp._tls.<dominio>` = `v=TLSRPTv1; rua=mailto:<MAIL_TLSRPT_RUA>` (solo si esa variable
   opcional está configurada y es una sola dirección válida; a diferencia de `MAIL_DMARC_RUA`, no es obligatoria). Los
   verifica (un solo TXT de cada; el id tiene que ser el vigente; la lista `rua` tiene que incluir la de la plataforma), los
   publica el proveedor DNS automático y `dns_checks` los admite (`06_mta_sts_records.sql`). No se pide el CNAME
   `mta-sts` del punto 4 del diseño: su destino lo fija el borde. Desactivar la política no retira el TXT del proveedor.
9. **Interfaz.** Panel MTA-STS en la ficha del dominio (`web/src/pages/domains/MtaStsCard.tsx`), con el modo, la versión,
   la vigencia, los pasos que da el servicio (`allowed_modes`) y `ConfirmDialog` con el aviso de que `enforce` puede dejar
   de entregar correo.

Sigue pendiente, con su responsable: el borde (P: quien opera el servidor), el buzón de `MAIL_TLSRPT_RUA` y el panel de
informes DMARC y TLS-RPT (A3, "se hace después"), la comprobación del certificado al pasar a `enforce`, y un paso de
confirmación de identidad (step-up) en el servicio para `enforce`, hoy solo de interfaz.
