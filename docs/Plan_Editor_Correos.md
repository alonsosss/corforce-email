# Editor visual de correos y verificador de entregabilidad

Estado a 2026-09-23. Decision en `docs/adr/0012-editor-visual-de-correos-con-grapesjs-y-mjml.md`. Este
documento fija los contratos entre las piezas (para construirlas en paralelo) y es el registro de estado
(seccion 8).

## 1. Objetivo

Un editor tipo lienzo para disenar correos de marketing y transaccionales con todas las herramientas
habituales, que produzca HTML compatible con Gmail, Outlook y Apple Mail y adaptable al movil, y que no deje
publicar lo que los proveedores castigan.

## 2. Piezas

| Pieza | Donde | Que hace |
|---|---|---|
| Editor | `web/` | GrapesJS + grapesjs-mjml: bloques, estilos, capas, deshacer, vista movil/escritorio/oscuro, kit de marca, imagenes, editor de imagenes, galeria, panel de entregabilidad |
| Diseno guardado | `templates` | El documento del editor junto a cada version |
| Kit de marca | `templates` | Logo, colores, tipografias y pie legal por empresa |
| Imagenes | `templates` + `pkg/objectstore` + `gateway` | Subida analizada con ClamAV, guardada por contenido, servida con cache inmutable |
| Verificador | `templates` | Reglas sobre el HTML renderizado; bloquea la publicacion de marketing con errores |
| Puntuacion antispam | `mail-security` | `POST /internal/mail-security/spam-check`, Rspamd `/checkv2` de la celda |
| Almacen de objetos | `docker-compose.selfhosted.yml` | MinIO en la red interna, claves en el almacen de secretos |

## 3. Contratos de `templates` (bajo `/api/v1/templates`, permisos del modulo `templates`)

### 3.1 Diseno del editor en la version

Crear y editar un borrador (`POST .../{id}/versions`, `PUT .../{id}/versions/{v}` o los que ya existan)
aceptan, ademas de lo actual, un campo opcional:

```json
"editor": { "kind": "grapesjs-mjml", "project": { ... }, "mjml": "<mjml>...</mjml>" }
```

* `kind`: por ahora solo `grapesjs-mjml`; `null` o ausente = HTML a mano (lo de hoy).
* `project`: el `getProjectData()` de GrapesJS, opaco para el servidor. `mjml`: el MJML fuente.
* Tope del objeto serializado: 2 MiB (`VALIDATION_ERROR` si se supera). Una version publicada es inmutable
  tambien en su `editor`.
* `GET` de la version lo devuelve igual. El HTML sigue siendo obligatorio y es lo que se renderiza y envia.

### 3.2 Kit de marca

`GET /brand-kit` (permiso `brand_kit.read`) y `PUT /brand-kit` (`brand_kit.update`):

```json
{
  "logo_asset_id": "uuid|null",
  "colors": ["#0B5FFF", "#111827"],
  "fonts": ["Inter", "Georgia"],
  "footer": { "company": "Acme SAC", "address": "Av. Siempre Viva 742, Lima, Peru",
              "website": "https://acme.pe", "support_email": "hola@acme.pe" },
  "updated_at": "..."
}
```

Colores `#RRGGBB` (hasta 12), tipografias de una lista cerrada de seguras para correo con alternativa (hasta
6), direccion obligatoria para publicar marketing (regla `missing_physical_address`). Sin kit, `GET`
devuelve el objeto vacio con `updated_at: null`.

### 3.3 Imagenes

* `POST /assets` (`assets.create`), `multipart/form-data` campo `file`: PNG, JPEG, GIF o WebP comprobados
  por su firma (no por la extension ni el Content-Type), hasta 5 MiB, dimensiones hasta 4000x4000.
  Analizada con ClamAV antes de guardar (sin ClamAV configurado: 503 `SCANNER_UNAVAILABLE`; infectada: 422
  `ASSET_REJECTED`). Clave `public/<tenant>/templates/<sha256>.<ext>`: la misma imagen no se guarda dos veces.
  Respuesta `201`: `{ "id", "url", "content_type", "size_bytes", "width", "height", "name", "created_at" }`.
  `url` es absoluta: `<PUBLIC_BASE_URL>/media/public/<tenant>/templates/<sha256>.<ext>`.
* `GET /assets?limit=&cursor=` (`assets.read`): las de la empresa, mas recientes primero.
* `DELETE /assets/{id}` (`assets.delete`): la retira del listado; el objeto NO se borra, porque correos ya
  enviados lo muestran.

### 3.4 Verificacion de entregabilidad

* `POST /check` (`templates.read`): verifica un contenido sin guardarlo, para el panel en vivo del editor.
  Cuerpo `{ "kind": "marketing|transactional", "subject", "html", "text"?, "variables"?: {..} }`.
* `POST /{id}/versions/{v}/check` (`templates.read`): verifica una version guardada.
* Respuesta:

```json
{
  "passed": false,
  "issues": [ { "code": "missing_alt", "severity": "warning", "message": "...", "count": 2 } ],
  "stats": { "html_bytes": 48211, "text_chars": 912, "images": 4, "links": 11, "text_image_ratio": 0.71 },
  "spam": { "available": true, "score": 1.8, "required": 15, "action": "no action",
            "symbols": [ { "name": "MIME_HTML_ONLY", "score": 0.2, "description": "..." } ] }
}
```

* `passed` es falso si hay algun `error`. Publicar (`POST .../{v}/publish`) una version de `kind=marketing`
  con errores responde `409 DELIVERABILITY_FAILED` con las `issues`. En transaccional los errores de marketing
  (baja, direccion) no aplican; el resto si.
* Sin `mail-security` o sin respuesta: `spam.available=false` y la verificacion sigue.

### 3.5 Reglas

| Codigo | Severidad | Regla |
|---|---|---|
| `missing_unsubscribe` | error (marketing) | El HTML no usa `{{.unsubscribe_url}}` en un enlace |
| `missing_physical_address` | error (marketing) | Sin direccion en el kit de marca ni en el cuerpo |
| `html_too_large` | error | HTML renderizado de mas de 102 KB: Gmail lo recorta y oculta la baja |
| `image_only` | error | Hay imagenes y menos de 200 caracteres de texto visible |
| `link_shortener` | error | Enlaces a acortadores publicos (bit.ly, tinyurl, t.co, goo.gl, ow.ly, is.gd, buff.ly, cutt.ly, rebrand.ly, shorturl.at) |
| `deceptive_link` | error | El texto visible de un enlace es una URL de otro dominio que su destino |
| `forbidden_content` | error | script, iframe, form, on*, javascript: (lo que ya rechaza el render) |
| `low_text_ratio` | aviso | Proporcion texto/imagen por debajo de 0,6 |
| `missing_alt` | aviso | Imagenes sin `alt` |
| `missing_preheader` | aviso | Sin texto de previsualizacion oculto al principio del cuerpo |
| `subject_all_caps` / `subject_punctuation` / `subject_too_long` / `subject_spam_words` | aviso | Asunto en mayusculas, con `!!`/`$$`, de mas de 78 caracteres o con palabras tipicas de spam (lista es/en) |
| `insecure_link` | aviso | Enlaces `http://` |
| `too_many_links` | aviso | Mas de 50 enlaces |
| `width_too_large` | aviso | Anchos fijos por encima de 640 px |
| `external_stylesheet` | aviso | `<link rel=stylesheet>` o `@import` (los clientes los ignoran) |
| `spam_score_high` | aviso / error | Puntuacion de Rspamd >= 5 aviso, >= la de rechazo (action reject) error |

## 4. Contrato de `mail-security`

`POST /internal/mail-security/spam-check` (token del gateway interno, sin sesion), cuerpo
`{ "message": "<MIME completo, hasta 2 MiB>" }` -> `200 { "score", "required", "action", "symbols": [...] }`.
Llama al `/checkv2` de Rspamd con la contrasena de solo lectura; nunca aprende (no es `/learn*`). Metodo y
limites como las demas rutas internas del servicio. `templates` la llama con `SPAM_CHECK_URL` (el
`mail-security` de la celda base) y un tiempo maximo de 10 s.

Implementado (V, 2026-09-23): la respuesta va en el sobre habitual, `200 {"data": {"score": 1.8,
"required": 15, "action": "no action", "symbols": [{"name", "score", "description"}]}}`, puntuaciones como
numeros JSON y simbolos del mas pesado al mas ligero (`description` vacia si Rspamd no la tiene). Errores:
`400 BAD_REQUEST` (JSON invalido o campos desconocidos), `400 MESSAGE_REQUIRED` (sin `message` o vacio),
`413 MESSAGE_TOO_LARGE` (mas de 2 MiB), `503 SPAM_CHECK_UNAVAILABLE` (Rspamd caido, 5xx o respuesta
ilegible) y `503 NOT_CONFIGURED` (sin `RSPAMD_CONTROLLER_PASSWORD` o rechazada). `/checkv2` se llama en el
controller: Rspamd 4.1.4 lo atiende con la contrasena de lectura, sin variable nueva. Plazo de 8 s. Sin
usuario, la ruta no pasa por el filtro de celda (la empresa en `X-Tenant-ID`, si la manda, se ignora): no
toca datos de ninguna empresa. Comparte el limite de 120 peticiones por minuto e IP del servicio.
Contrato con los motores en `deploy/mail/README.md` (Controller de Rspamd).

## 5. Imagenes servidas por el gateway

`GET /media/public/*` deja de redirigir a una URL prefirmada: lee el objeto del almacen y lo sirve con
`Content-Type` de la lista cerrada de imagenes, `Cache-Control: public, max-age=31536000, immutable` (la clave
lleva el sha256), `X-Content-Type-Options: nosniff` y `Content-Security-Policy: default-src 'none'`. Solo
`public/`, como hoy. MinIO no se publica en el borde.

## 6. Editor (web)

* Ruta de pantalla completa `/templates/:id/editor` (y desde el detalle de la plantilla, "Abrir editor").
* Bloques: seccion, columnas (1-4), texto, titulo, imagen, boton, separador, espaciador, redes sociales,
  video (miniatura con enlace), codigo de descuento, pie legal (del kit), preheader.
* Paneles: estilos, capas, ajustes del bloque, kit de marca, imagenes (subir, elegir, editar), variables del
  contacto para insertar (`{{.first_name}}`...) tambien dentro de URLs, verificacion en vivo (llama a
  `POST /check` con retraso), vista movil / escritorio / modo oscuro, envio de prueba (el existente).
* Galeria de al menos 6 plantillas prediseñadas (bienvenida, promocion, boletin, evento, carrito,
  transaccional) en MJML, versionadas en `web/`.
* Editor de imagenes: recortar, girar, redimensionar, filtros; guarda una imagen nueva por `POST /assets`.
* Todo compatible con la CSP actual (sin `unsafe-eval`), comprobado en un navegador real.

## 7. Orden de despliegue

Registro (permisos nuevos de `templates`) -> empresa (`templates`) -> MinIO y sus claves -> `mail-security`
-> `templates` -> `gateway` -> `web`.

## 8. Registro de estado

| Pieza | Estado |
|---|---|
| ADR y plan | Hecho |
| Backend `templates` (diseno, kit, imagenes, verificador) | En curso |
| `mail-security` spam-check | Hecho (V 2026-09-23, unitarias con Rspamd falso; contra Rspamd real pendiente de `make e2e-mail`, P) |
| MinIO y gateway | En curso |
| Editor web | En curso |
| Despliegue | Pendiente |
