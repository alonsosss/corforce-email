# ADR 0017: Aprovisionamiento de empresas desde otro producto

Fecha: 2026-09-25
Estado: aceptada

## Contexto

El ERP (`github.com/alonsosss/ERP`, mismo dueño, despliegue aparte) es multiempresa y hoy manda
correo por su cuenta: genera DKIM, SPF y DMARC en `crm_email.domains`, se los enseña al cliente para
que los publique, y envía por SMTP contra SES con credenciales por empresa en
`notification.email_provider`. No tiene rebotes, ni quejas, ni lista de supresión, ni reputación por
carril, ni recepción. Todo eso ya existe aquí.

Al mismo tiempo, el ERP **sí** sabe publicar DNS: gestiona los dominios de las tiendas en la cuenta
de Cloudflare de la plataforma, con su token cifrado en `organization.platform_credentials`.

Queremos que el ERP use esta plataforma como su motor de correo, para muchas empresas, sin
intervención manual en cada alta. Y queremos que los dos sistemas sigan separados: distinto dominio
de fallo, distinta superficie de ataque (aquí entran adjuntos no confiables por los puertos 25, 465,
587 y 993) y distinta forma de escalar.

El obstáculo concreto: hoy una clave de API solo sirve para **enviar** (tres rutas en
`api_key_routes` de `services/gateway/routes.json`) y solo la crea una persona con sesión **en esa
empresa**. No hay forma de que un programa externo dé de alta una empresa y obtenga su credencial de
envío.

## Decisión

Una **segunda familia de credencial**, de aprovisionamiento, disjunta de la de envío.

* **Prefijo propio** (`cfp_` frente a `cfm_`) y campo `kind` en `access_control.api_keys`.
* **Poderes disjuntos, por diseño:**
  * la de **envío** manda correo y no gestiona credenciales (`rejectAPIKeyCaller` ya lo impone);
  * la de **aprovisionamiento** crea empresas, da de alta dominios y emite claves de envío, y **no
    puede mandar un solo correo ni leer un buzón**.
* **Acotada por propiedad.** Una empresa creada por una credencial de aprovisionamiento queda marcada
  con esa credencial (`organization.tenants.provisioned_by`) y con la referencia externa que trae el
  llamador (`external_ref`, el UUID que el ERP ya tiene). Esa credencial **solo puede operar sobre las
  empresas que ella misma creó**: una filtración no alcanza a las empresas dadas de alta por otra vía.
* **Idempotente por `external_ref`.** Reintentar un alta devuelve la misma empresa, no crea otra.
* La dependencia va en **un solo sentido**: el ERP llama aquí; esta plataforma no conoce al ERP ni a
  su base. El aviso de vuelta (entregado, rebotado) es un webhook firmado y reintentable que, si el
  ERP no responde, no bloquea nada.

### Quién valida el correo

Todo el correo se valida **aquí**: propiedad del dominio, DKIM, SPF, DMARC, MX, MTA-STS, identidad
en SES, rebotes, quejas, supresión, reputación y el permiso de enviar desde una dirección. Al ERP le
queda una API de conexión: crear la empresa y mandar correo. Nada más.

El motivo no es de reparto de trabajo sino de acoplamiento: si el ERP publicara los registros de
correo, tendría que aprender de MX, selectores DKIM y reverificación, y cada cambio de esas reglas
obligaría a desplegar los dos productos a la vez.

### Quién publica el DNS

Esta plataforma, con el proveedor DNS automático que ya existe (`domain-service`, `dns_mode:
cloudflare`). Las zonas de los clientes del ERP viven en **su** cuenta de Cloudflare, porque les pide
delegar los nameservers, así que el ERP entrega un token al dar de alta el dominio.

Ese token es **acotado a esa zona y solo a esa**, creado por el ERP al vuelo (`POST /user/tokens` de
Cloudflare) con los dos permisos que pide la plataforma: `Zone — DNS — Edit` y `Zone — Zone — Read`.
Aquí se guarda cifrado (`MAIL_ENCRYPTION_KEY`), como cualquier credencial de tercero, y se puede
rotar o revocar sin tocar el resto.

Lo que **no** se acepta es el token de cuenta del ERP, que alcanza todas las zonas. Crear tokens exige
a su vez una credencial con permiso para crearlos, que se queda en el ERP y nunca cruza: lo único que
cruza es un token que solo puede escribir el DNS de un cliente.

Para un dominio que no delega en esa cuenta queda el modo manual, que ya funciona: la plataforma
devuelve los registros y los publica quien corresponda.

### De quién cuelgan las claves de envío

De una **cuenta de servicio por empresa**, creada por el aprovisionamiento, sin contraseña y sin poder
iniciar sesión.

El alcance de una clave se acota al de quien la creó y muere con esa cuenta (`effectiveScopes`). Si
colgara del administrador real del cliente, desactivar a esa persona dejaría de enviar a su tienda
sin ninguna señal evidente. La cuenta de servicio no la desactiva nadie por error y deja claro en la
auditoría que quien envía es una integración, no una persona.

### Transporte

HTTPS siempre. Sobre las rutas de aprovisionamiento, además, **certificado de cliente (mTLS)** con la
CA interna (`ops/security/internal-tls.sh`), o en su defecto una lista de IP en el borde. Con eso la
superficie de aprovisionamiento no existe para quien no presente certificado, ni para probar
credenciales.

## Alternativas descartadas

* **Una clave maestra que envíe como cualquier empresa.** Una filtración comprometería a todos los
  clientes a la vez, y el correo saliente dejaría de tener dueño identificable.
* **Ampliar la clave de envío para que también gestione claves.** Contradice `rejectAPIKeyCaller`, que
  existe justo para que una credencial que vive en el servidor de un cliente no pueda emitir otras.
* **Que el ERP escriba en la base de esta plataforma.** No es separar, es acoplar por detrás: cualquier
  cambio de esquema aquí rompe el ERP, y el aislamiento por empresa deja de estar garantizado.
* **Que esta plataforma llame al ERP para saber qué empresas existen.** Invierte la dependencia y deja
  el correo sin funcionar cuando el ERP no está.
* **Que el ERP publique los registros de correo con el token que ya tiene.** Evita que cruce un
  secreto, pero mete en el ERP el conocimiento de MX, DKIM y reverificación, y ata los despliegues de
  los dos productos. Se descarta por acoplamiento, no por seguridad.
* **Que el ERP entregue su token de cuenta de Cloudflare.** Una filtración aquí alcanzaría el DNS de
  todos sus clientes, incluidas sus tiendas.

## Consecuencias

* El ERP deja de mantener DKIM y envío propios (`crm_email` y el `SMTPSender` de `notification`). Hasta
  que lo haga, conviven: dos selectores DKIM distintos publicados no se estorban, pero **el DNS de
  correo de un dominio tiene un solo dueño**, y pasa a ser esta plataforma.
* Aparece una credencial muy poderosa (crea empresas). Se compensa con: poderes disjuntos, acotación
  por propiedad, mTLS, auditoría de cada llamada con el ERP como actor, cupos y rotación con dos
  credenciales válidas a la vez.
* Hay un camino intermedio que da valor sin escribir código: apuntar `notification.email_provider` de
  una empresa del ERP a `smtp.core-force.com:2525` con una clave de envío de esa empresa. El
  `SMTPSender` del ERP no cambia y ya gana rebotes, supresión y reputación.
