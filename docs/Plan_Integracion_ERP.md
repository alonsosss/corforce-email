# Plan: el ERP envía por Core Force Mail

Decisión y alternativas descartadas en `docs/adr/0017-aprovisionamiento-desde-otro-producto.md`.
Aquí va el contrato, las fases y el estado.

## 1. Dónde queda la frontera

**Todo el correo se valida aquí. El ERP solo tiene una API de conexión.**

| | Quién |
|---|---|
| Propiedad del dominio, DKIM, SPF, DMARC, MX, MTA-STS | Correo |
| Publicar el DNS, verificar y reintentar hasta lograrlo | Correo |
| Identidad en SES, rebotes, quejas, supresión, reputación | Correo |
| Decidir si una empresa puede enviar y desde qué dirección | Correo |
| Crear la empresa y pedir "manda este correo" | ERP |

El ERP deja de mantener DKIM, SES y verificación de dominios, y gana rebotes, quejas, lista de
supresión, reputación por carril, seguimiento de aperturas y clics, y buzones si algún día los
quiere. Esta plataforma gana su primer consumidor programático y el contrato que servirá para el
siguiente producto.

## 2. Fase A: valor inmediato, sin código

El `SMTPSender` del ERP (`services/notification/internal/adapters/email/sender.go`) habla SMTP
contra SES. Apuntarlo aquí no exige tocarlo: en `notification.email_provider` de una empresa,

| Campo | Valor |
|---|---|
| host | `smtp.core-force.com` |
| puerto | `2525` (STARTTLS) |
| usuario | el prefijo de una clave de envío de esa empresa |
| contraseña | la clave completa (`cfm_...`) |

Esa empresa pasa a enviar por aquí con todo lo anterior incluido. Sirve para probar el camino
entero con una sola empresa antes de invertir en la automatización.

El puerto es `2525` y no el de TLS implícito: su `SMTPSender` usa `net/smtp.SendMail`, que abre en
claro y negocia STARTTLS. Contra 2465 se queda esperando el saludo y agota el tiempo.

Requisitos en el ERP, los tres bloqueantes y ninguno evidente desde su pantalla:

* El **remitente** (`notification.email_settings`, «Correo remitente») tiene que estar puesto: sin él
  `buildEmailConfig` usa el usuario SMTP como dirección de `From`, y aquí el usuario es el prefijo de
  la clave, que no es una dirección. El relay lo rechaza en `MAIL FROM`.
* El superadministrador del ERP tiene que **habilitar la cuenta propia** de esa empresa
  (`own_ses_allowed`): sin eso `GetEffective` descarta la fila de la empresa y usa la cuenta global.
* El dominio, dado de alta **aquí** con propósito `sending` o `both` y verificado.

Sus dos emisores (el transaccional de `notification` y el de campañas de `crm-email`) leen la misma
fila, así que el cambio los mueve a los dos a la vez. Sus cabeceras `X-SES-CONFIGURATION-SET` no
estorban: `pkg/rawmail` las retira antes de entregar a SES.

## 3. Fase B: la credencial de aprovisionamiento

Segunda familia de credencial (ADR 0017): prefijo `cfp_`, `kind = provisioning` en
`access_control.api_keys`. No envía correo; crea empresas, dominios y claves de envío, y solo sobre
las empresas que ella misma creó.

### Contrato

Todo bajo `/api/v1/provisioning`, con `Authorization: Bearer cfp_...`.

#### `POST /provisioning/tenants`

```json
{"external_ref": "<uuid de la empresa en el ERP>", "slug": "campovivo",
 "name": "Campovivo Alimentos", "settings": {"tz": "America/Lima"}}
```

Idempotente por `external_ref`: repetirlo devuelve la misma empresa (200) en vez de crear otra (201).
Devuelve `{id, external_ref, slug, name, status}`.

#### `POST /provisioning/tenants/{external_ref}/domains`

```json
{"domain": "campovivoalimentos.com", "purpose": "sending",
 "dns": {"provider": "cloudflare", "api_token": "<token acotado a esa zona>"}}
```

Con `dns`, la plataforma **publica los registros ella misma**, los verifica y reintenta hasta que el
dominio quede verificado: el ERP no vuelve a saber de MX ni de DKIM. El token va acotado a esa zona y
solo a esa (ADR 0017), se guarda cifrado y se puede rotar.

Sin `dns`, modo manual: devuelve **los registros DNS exactos que hay que publicar**, los mismos que
`GET /api/v1/domains/{id}`, y los publica quien corresponda.

#### `GET /provisioning/tenants/{external_ref}/domains/{domain}`

Estado de verificación y los registros que faltan, para que el reconciliador del ERP reintente.

#### `POST /provisioning/tenants/{external_ref}/domains/{domain}/verify`

Fuerza la comprobación en cuanto el ERP ha publicado el DNS, sin esperar al barrido.

#### `POST /provisioning/tenants/{external_ref}/api-keys`

```json
{"name": "ERP - envío transaccional", "expires_at": null}
```

Emite una clave **de envío** (`cfm_...`) de esa empresa, con el alcance de enviar y leer el estado de
un mensaje, colgada de la **cuenta de servicio** de esa empresa y no de una persona (ADR 0017). El
secreto se devuelve **una sola vez**: el ERP lo guarda cifrado por empresa, como ya hace con Meta Ads
o Google Ads. El alta de la empresa ya devuelve la primera, así que esta ruta es para rotar.

#### `POST /provisioning/tenants/{external_ref}/api-keys/{id}/revoke`

Para rotar sin pasar por la web.

### Lo que la credencial de aprovisionamiento NO puede hacer

Mandar correo, leer buzones, tocar una empresa que no creó, ver el contenido de los mensajes, ni
emitir otra credencial de aprovisionamiento.

## 4. Fase C: lo que cambia en el ERP

Poco, que es el objetivo:

1. **Crear un token de Cloudflare acotado a la zona del cliente** (`POST /user/tokens`) al dar de alta
   su dominio, con `Zone — DNS — Edit` y `Zone — Zone — Read` sobre esa zona y ninguna más. La
   credencial que crea tokens se queda en el ERP y nunca cruza.
2. **Enganchar el alta.** En `CreateTenant` (`services/organization/internal/app/usecase.go`), tras
   sembrar la empresa, llamar a las dos rutas de arriba y guardar la clave cifrada por empresa. Si la
   llamada falla, el alta **no** se revierte: queda pendiente y se reintenta.
3. **Reintento.** Basta con reintentar el alta pendiente: la verificación y la reconciliación del DNS
   son de esta plataforma, no del ERP.
4. **Enviar por aquí.** `notification.email_provider` con los datos de la fase A, o mejor, migrar a la
   API (`POST /api/v1/transactional/messages`), que da id de mensaje y estado.
5. **Retirar lo propio:** el DKIM y la verificación de `crm_email`, y el envío directo a SES.

Lo que **no** hay que tocar: el cliente de Cloudflare de las tiendas, que sigue igual. El ERP no
aprende nada de MX ni de DKIM.

## 5. Seguridad

Cuatro capas, de fuera hacia dentro:

1. **Transporte:** HTTPS y, sobre `/provisioning`, certificado de cliente o lista de IP en el borde.
2. **Identidad:** credencial de máquina propia del ERP, con dos válidas a la vez para rotar.
3. **Autorización:** poderes disjuntos de la credencial de envío, y acotación por propiedad
   (`provisioned_by`).
4. **Credenciales de envío separadas por empresa**, cifradas en el ERP, colgadas de la cuenta de
   servicio de cada empresa y revocables una a una.
5. **El token de DNS que cruza es de una sola zona**, cifrado aquí con `MAIL_ENCRYPTION_KEY`, rotable
   y revocable desde el ERP sin tocar nada más.

Y encima: idempotencia por `external_ref`, auditoría de cada llamada con el ERP como actor, cupos
por credencial y métricas propias.

## 6. Riesgos

* **Una credencial que crea empresas es poderosa.** Lo compensan las cuatro capas y, sobre todo, la
  acotación por propiedad: el peor caso es crear empresas de más, no tocar las existentes.
* **Dos sistemas firmando el mismo dominio** mientras dure la convivencia. Se evita con la regla de un
  solo dueño del DNS de correo, y con el orden de la fase C5.
* **El alta del ERP pasa a depender de una llamada de red.** Por eso no revierte: el alta se completa
  igual y el correo queda pendiente, que es lo que ya pasa hoy con los dominios de tienda.
* **Cupo de SES compartido.** Todas las empresas del ERP comparten la cuota de la cuenta (50.000 al
  día). La reserva del carril transaccional (`SES_MARKETING_QUOTA_RESERVE`) ya protege lo crítico,
  pero con volumen habrá que pedir ampliación a AWS.

## 7. Estado

**APARCADO el 2026-09-25.** La base está hecha y no cambia el comportamiento de nada (no hay ninguna
ruta de aprovisionamiento declarada). Se retoma cuando estén cerrados los puntos 1 a 3 de
`docs/Plan_Robustez_Operativa.md`: el ensayo de recuperación completa, la puerta de la CI y la
rotación de lo expuesto. El motivo es de orden, no de diseño: antes de sumar una integración que crea
empresas conviene saber cuánto se tarda en volver de un desastre.

Por dónde se retoma: fase B2, que empieza por la migración de registro con `external_ref` y
`provisioned_by` en `organization.tenants`, y la cuenta de servicio por empresa.


| Fase | Estado |
|---|---|
| A. Envío por SMTP sin código | Disponible hoy: falta elegir la empresa del ERP con la que probar |
| B1. Familia de credencial y separación de poderes | Hecho (`db3cbe2`): `kind` en las claves, prefijo `cfp_`, dos listas de rutas disjuntas en el gateway, con pruebas de los dos sentidos. Sin rutas de aprovisionamiento todavía |
| B2. Rutas de aprovisionamiento y cuenta de servicio | Aparcado (2026-09-25) |
| C. Cambios en el ERP | Aparcado (2026-09-25), en el otro repositorio |

## 8. Estado real tras la fase A (2026-09-25)

Comprobado contra producción, no contra el informe del otro producto:

| Qué | Cómo quedó |
|---|---|
| `avisos.core-force.com` | Verificado, `sending_ready`, identidad, DKIM y MAIL FROM correctos en SES |
| Clave de Campovivo (`u5kw3lf4ikjv`) | Viva, su empresa, en uso |
| Clave global del otro producto (`dn6enxxxlear`) | Viva, **empresa plataforma**, en uso |
| Envíos por el relay | Tres, ninguno rechazado; el de Campovivo sale alineado con su dominio |

**Lo que no queda bien y hay que corregir: la cuenta global cuelga de la empresa plataforma.**
Las empresas del otro producto que no tienen cuenta propia envían con la credencial de la empresa
plataforma, así que comparten entre ellas —y con el correo operativo de esta plataforma, que sale
del mismo `avisos.core-force.com`— la lista de supresión, el estado de reputación, el cupo y la
auditoría. Una queja provocada por una de ellas degrada el correo de recuperación de contraseña de
esta plataforma, y revocar esa clave las corta todas a la vez.

Es el mismo problema que la fase B2 resuelve de raíz (una empresa aquí por cada empresa de allá).
Mientras tanto, el paso barato es una empresa propia para el otro producto, con su dominio de envío
y su clave, de modo que su reputación no toque la de esta plataforma.

También conviene saber que, por la cuenta global, el nombre visible y la dirección de respuesta
solo aparecen si esa empresa rellenó su remitente en el otro producto. Sin eso, su correo sale como
la plataforma y sin a quién responder.
