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
  * la de **aprovisionamiento** crea empresas, da de alta dominios, devuelve los registros DNS que
    hay que publicar y emite claves de envío, y **no puede mandar un solo correo ni leer un buzón**.
* **Acotada por propiedad.** Una empresa creada por una credencial de aprovisionamiento queda marcada
  con esa credencial (`organization.tenants.provisioned_by`) y con la referencia externa que trae el
  llamador (`external_ref`, el UUID que el ERP ya tiene). Esa credencial **solo puede operar sobre las
  empresas que ella misma creó**: una filtración no alcanza a las empresas dadas de alta por otra vía.
* **Idempotente por `external_ref`.** Reintentar un alta devuelve la misma empresa, no crea otra.
* La dependencia va en **un solo sentido**: el ERP llama aquí; esta plataforma no conoce al ERP ni a
  su base. El aviso de vuelta (entregado, rebotado) es un webhook firmado y reintentable que, si el
  ERP no responde, no bloquea nada.

### Quién publica el DNS

El ERP. Esta plataforma devuelve los registros exactos y **verifica**; el ERP los publica con el
token de Cloudflare que ya tiene. Así ningún secreto cambia de manos y el token sigue donde estaba.
La alternativa (que el ERP nos entregue su token por empresa, como hace un cliente final con
`POST /domains/dns-providers/cloudflare/connect`) queda disponible, pero no es la recomendada para
el ERP: su token alcanza todas las zonas de la cuenta.

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

## Consecuencias

* El ERP deja de mantener DKIM y envío propios (`crm_email` y el `SMTPSender` de `notification`). Hasta
  que lo haga, conviven: dos selectores DKIM distintos publicados no se estorban, pero **el DNS de
  correo de un dominio tiene un solo dueño**, y pasa a ser esta plataforma.
* Aparece una credencial muy poderosa (crea empresas). Se compensa con: poderes disjuntos, acotación
  por propiedad, mTLS, auditoría de cada llamada con el ERP como actor, cupos y rotación con dos
  credenciales válidas a la vez.
* Hay un camino intermedio que da valor sin escribir código: apuntar `notification.email_provider` de
  una empresa del ERP a `smtp.core-force.com:2465` con una clave de envío de esa empresa. El
  `SMTPSender` del ERP no cambia y ya gana rebotes, supresión y reputación.
