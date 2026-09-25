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
| puerto | `2465` (TLS implícito) o `2525` (STARTTLS) |
| usuario | el prefijo de una clave de envío de esa empresa |
| contraseña | la clave completa (`cfm_...`) |

Esa empresa pasa a enviar por aquí con todo lo anterior incluido. Sirve para probar el camino
entero con una sola empresa antes de invertir en la automatización.

Requisito: su dominio dado de alta aquí con propósito `sending` o `both` y verificado.

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

### 8.1 Remitente de las empresas sin configurar (2026-09-25)

Comprobado en `transactional.messages` de la empresa de plataforma: por la clave global salió hoy
«Prueba de correo (ERP)» como `Core Force <notificaciones@avisos.core-force.com>` y sin `Reply-To`.
El destinatario no sabe qué empresa le escribe y, si contesta, la respuesta se pierde
(`avisos.core-force.com` no tiene MX). La dirección de envío no puede cambiar (no es su dominio),
pero el nombre visible y la respuesta sí.

La causa está en el otro producto (`services/notification/internal/app/usecase.go`,
`buildEmailConfig`): por la cuenta global usa el nombre y la respuesta de
`notification.email_settings` de la empresa, y si no los tiene sale el nombre de la plataforma sin
respuesta. Corrección, en el otro producto y a cargo de su sesión:

1. Rellenar en cada empresa que envía por la cuenta global el nombre visible (su nombre comercial) y
   el responder-a (su correo de contacto), con sus datos reales, y mandar la prueba desde su pantalla.
2. Red de seguridad en `buildEmailConfig`: sin nombre configurado, el de la empresa; sin respuesta
   configurada, su correo de contacto. Nunca el nombre de la plataforma para una empresa que no lo es.

Se da por cerrado cuando cada prueba aparece aquí con su nombre, su `Reply-To` y `delivered`.

### 8.2 Umbral para separar la empresa compartida

No se separa por fecha sino por umbral. Mientras sean las empresas del propio usuario mandando avisos,
la cuenta compartida no es un riesgo, y separarla ahora sería trabajo que la fase B2 tira. Se separa
(una empresa propia para el otro producto, con su dominio de envío y su clave) en cuanto pase
cualquiera de estas dos cosas:

| Disparador | Cómo se detecta |
|---|---|
| El otro producto manda correo a clientes finales en volumen (campañas, no avisos internos) | Alertas `CuentaCompartidaEnviaMasivo` (un solo mensaje masivo por la cuenta global en 24 h: `List-Unsubscribe`, `List-Id` o `Precedence: bulk`) y `CuentaCompartidaConVolumen` (más de 300 en 24 h). Métrica `transactional_relay_messages_total{account,class}`; necesita `PLATFORM_TENANT_ID` en `transactional` |
| Entra en el otro producto una empresa que no es del usuario | No se ve desde aquí: es un paso del alta de empresas del otro producto, que debe avisar antes de darle envío por la cuenta global |

Cualquiera de los dos convierte «comparten reputación» en un riesgo real para el correo de
recuperación de contraseña de esta plataforma.
