# Migración de un dominio que ya tiene correo en otro proveedor

Un dominio que llega a la plataforma casi nunca llega vacío: ya recibe correo en Hostinger, Google
Workspace, Microsoft 365 o el hosting de turno, con buzones en uso. Apuntar su MX aquí y crear los
buzones después deja un hueco en el que todo lo que llegue se rechaza, y mover todos los buzones a la
vez obliga a coordinar a la empresa entera en una tarde.

Este documento describe la alternativa que la plataforma soporta y que es la forma normal de hacerlo:
**el MX apunta aquí desde el primer día y lo que todavía no está migrado se reenvía al proveedor
anterior**. Cada buzón se migra cuando conviene, sin ventana de corte y sin coordinar a nadie.

## 1. El modelo

Tres banderas del dominio en `mail-directory` (`mail.domains` de la celda) deciden el reparto:

| Bandera | Efecto |
|---|---|
| `backupmx` | El dominio es de reenvío: Postfix acepta correo para él aunque el destinatario no exista aquí |
| `relay_all_recipients` | Acepta **todos** los destinatarios del dominio y los manda al `relayhost` |
| `relay_unknown_only` | Los destinatarios que **sí** existen aquí se entregan en local (LMTP a Dovecot); el resto sale al `relayhost` |

Las tres juntas, con un `relayhost` que apunte al servidor del proveedor anterior, dan justo lo que se
busca: `ventas@` ya migrado se entrega aquí, `contabilidad@` todavía no migrado sigue llegando a su
buzón de siempre, y ninguno de los dos se entera. Lo resuelven las consultas
`pgsql_relay_recipient_maps.cf` y `pgsql_relay_ne.cf` de Postfix, sin nada específico por dominio.

Con `relay_unknown_only` **desactivado** el dominio se comporta como un simple relevo: todo sale al
proveedor anterior y aquí no se entrega nada. Sirve para probar el enrutado antes de migrar el primer
buzón.

## 2. Requisitos previos

* El dominio dado de alta en `domain-service` con propósito `corporate` (o `both` si además enviará
  campañas), y su clave DKIM generada.
* Acceso al DNS del dominio, por una de estas dos vías:
  * **Proveedor automático (recomendado).** La empresa conecta su propio token de Cloudflare en
    Dominios -> Proveedor DNS de la web. Solo pide `Zone — DNS — Edit` y `Zone — Zone — Read`, y el
    token se guarda cifrado (`MAIL_ENCRYPTION_KEY`), nunca en claro. A partir de ahí la plataforma
    publica y comprueba los registros sola (`dns_mode: cloudflare`, `POST /api/v1/domains/{id}/publish-dns`).
    El token se acota a **la zona de esa empresa y a ninguna más**: un token de cliente no debe poder
    tocar la zona de la plataforma.
  * **Manual.** La empresa publica los registros que da `GET /api/v1/domains/{id}` y la plataforma
    solo verifica.
* Los datos del proveedor anterior: su servidor de entrada (el MX que tiene hoy) y, para copiar el
  histórico, el servidor IMAP y las credenciales de cada buzón.

## 3. Procedimiento

### Paso 1. Publicar lo que no rompe nada

Estos dos registros conviven con el correo actual y se pueden publicar en cualquier momento:

| Tipo | Nombre | Valor |
|---|---|---|
| TXT | `_cfm-verify.<dominio>` | el `cfm-verify=...` que da `GET /api/v1/domains/{id}` |
| TXT | `<selector>._domainkey.<dominio>` | la clave DKIM de la plataforma |

Con el proveedor automático conectado, `publish-dns` los publica sin tocar el MX ni el SPF.

El DKIM del proveedor anterior usa otro selector y sigue publicado: **no se toca**. Dos selectores
conviven sin problema, que es lo que permite que ambos firmen durante la transición.

### Paso 2. Crear el transporte hacia el proveedor anterior

Antes de mover el MX, para que el reenvío exista cuando el correo empiece a llegar:

```
POST /api/v1/mail-routing/transports
{"destination": "<dominio>", "nexthop": "[mx1.hostinger.com]:25", "active": true}
```

`nexthop` entre corchetes para que Postfix entregue a ESE servidor y no vuelva a resolver el MX del
dominio, que ya apunta aquí: sin corchetes el correo se entrega a sí mismo y se queda en la cola.

**No es un `relayhost`.** El `relayhost` de un dominio (`relayhost_id`) gobierna por dónde SALE el
correo que envían sus buzones, no lo que entra: ponerlo saca el correo de los buzones ya migrados por
el servidor del proveedor anterior, que lo rechaza por no estar autorizado. En coexistencia el
dominio va **sin** `relayhost_id` y con el transporte de arriba.

### Paso 3. Bajar el TTL del MX

En el DNS del dominio, poner el TTL del MX (y del TXT del SPF) en 300 segundos **un día antes**. Si
algo hay que revertir, se revierte en cinco minutos y no en las horas que dure la caché anterior.

### Paso 4. Mover el MX y el SPF

| Tipo | Nombre | Valor |
|---|---|---|
| MX | `<dominio>` | `mx.core-force.com`, prioridad 10 (se retiran los del proveedor anterior) |
| TXT | `<dominio>` | `v=spf1 include:spf.core-force.com include:<spf del proveedor anterior> ~all` |

El SPF lleva **los dos** mientras dure la convivencia: los buzones ya migrados salen por aquí y los
que no, siguen saliendo por el proveedor anterior. Terminar en `~all` y no en `-all` hasta cerrar la
migración: un `-all` con un emisor olvidado tira correo legítimo.

El DMARC se deja en `p=none` durante la transición. Endurecerlo a `quarantine` es el último paso,
cuando ya se sabe que todo lo que sale del dominio está firmado.

### Paso 5. Activar el reenvío

En cuanto el MX apunte aquí, con el dominio ya verificado, dejar el dominio así (web: Dominios ->
el dominio -> Directorio; o `PATCH /api/v1/mail-domains/{id}`):

```
{"backupmx": true, "relay_all_recipients": true, "relay_unknown_only": true}
```

Desde este momento el correo del dominio entra por la plataforma y sale al proveedor anterior sin que
nadie note el cambio. **Comprobarlo antes de seguir**: enviar un correo a una dirección que todavía
vive en el proveedor anterior y verificar que llega a su buzón de siempre.

### Paso 6. Migrar buzón a buzón

Para cada buzón, por orden de menor a mayor importancia:

1. Crearlo en la plataforma con la misma dirección (web: Buzones -> Nuevo, o `POST /api/v1/mailboxes`).
   Desde que existe, su correo se entrega aquí y ya no se reenvía.
2. Copiar el histórico con la migración de buzones (`docs/adr/0002`): servidor IMAP del proveedor
   anterior, usuario y contraseña del buzón. Copia sin borrar el origen, así que se puede repetir.
3. Avisar a la persona de que cambie su cliente de correo al servidor de la plataforma.

Una segunda pasada de la migración al final del día recoge lo que llegó mientras tanto.

### Paso 7. Cerrar la migración

Cuando no quede ningún buzón en el proveedor anterior:

1. Quitar el reenvío: `{"backupmx": false, "relay_all_recipients": false, "relay_unknown_only": false}`
   y borrar el transporte del paso 2. A partir de aquí, una dirección que no exista se rechaza en la
   conexión, que es lo correcto.
2. SPF solo con la plataforma y en `-all`.
3. DMARC a `p=quarantine` con `rua` al buzón de informes.
4. Subir de nuevo el TTL del MX.
5. Retirar el DKIM del proveedor anterior cuando lleve unos días sin firmar nada.

## 4. Comprobaciones

Con los mapas que consulta Postfix, sin enviar nada (en el contenedor `postfix-mail`, `postmap -q`;
un resultado vacío sale como `empty lookup result`):

| Consulta | Mapa | Resultado correcto |
|---|---|---|
| `<dominio>` | `pgsql_virtual_relay_domain_maps` | el dominio (es de reenvío) |
| `cualquiera@<dominio>` | `pgsql_relay_recipient_maps` | la propia dirección (se acepta) |
| `<buzón migrado>` | `pgsql_relay_ne` | `lmtp:inet:dovecot:24` (se entrega aquí) |
| `<dirección sin migrar>` | `pgsql_relay_ne` | vacío (sale al proveedor anterior) |
| `<dominio>` | `pgsql_transport_maps` | `smtp_via_transport_maps:[<mx del proveedor>]:25` |
| `<buzón migrado>` | `pgsql_sender_dependent_default_transport_maps` | `smtp:` a secas, **sin** el proveedor anterior |

Y con correo real, que es lo que cierra la migración:

1. A un buzón ya migrado: llega a su bandeja aquí (IMAP en `mx.core-force.com`).
2. A una dirección sin migrar: el registro de Postfix muestra `relay=<mx del proveedor>` y `status=sent`.
3. Desde un buzón migrado (SMTP autenticado en el 587): sale firmado, `DKIM-Signature` con
   `d=<dominio>` y el selector de la plataforma.

## 5. Lo que hay que vigilar

* **Rebotes de rebote.** Con `relay_all_recipients` la plataforma acepta el correo antes de saber si
  el destinatario existe en el proveedor anterior. Si este lo rechaza, el rebote lo genera la
  plataforma. Es el precio de no perder correo durante la migración y se acaba al cerrar el paso 7.
  Mientras dure, vigilar la cola de Postfix (`/api/v1/mail-security/queue`).
* **El certificado no cubre el dominio del cliente.** `acme` solo pide nombres de dominios que no son
  de reenvío (`SELECT domain FROM mail.domains WHERE NOT backupmx AND active`). Da igual: los clientes
  de correo se conectan a `mx.core-force.com`, que sí está en el certificado. Al cerrar el paso 7 el
  dominio deja de ser de reenvío y entra en la siguiente renovación.
* **Cuotas.** El histórico que se copia ocupa lo mismo que ocupaba en el proveedor anterior. Revisar
  la cuota del buzón antes de migrarlo, o la copia se corta a medias.
* **Un dominio, un MX.** No existe forma de que dos plataformas reciban a la vez el correo de un
  dominio: el reparto lo hace quien tiene el MX. Por eso el MX viene aquí desde el primer día, y es
  la plataforma la que decide qué entrega y qué reenvía.
