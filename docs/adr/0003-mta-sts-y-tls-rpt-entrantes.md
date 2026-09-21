# ADR 0003: MTA-STS y TLS-RPT para los dominios de las empresas

## Estado

Propuesto (2026-09-21). No implementado. Cierra el diseño de `Plan_Estrategico_Mejoras_Correo.md`, A3, en lo
que toca a MTA-STS y TLS-RPT. Depende de decisiones sobre el proxy de borde y los certificados que son de
quien opera el servidor.

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
