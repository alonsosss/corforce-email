# Plan: correos de pedido con lista de productos

El ERP (y cualquier otro producto) envía la confirmación de un pedido con **datos**, nunca con
HTML: `POST /api/v1/transactional/messages` con `template_id` y `variables`. El diseño vive aquí,
en `templates`, versionado y editable desde el editor visual. Este plan añade lo que faltaba para
que ese correo muestre los productos comprados, al estilo de una tienda grande, sin abrir huecos
de seguridad ni romper las reglas de Gmail.

## 1. El contrato

```json
{
  "from": {"email": "pedidos@tienda.example", "name": "Tienda"},
  "to": [{"email": "cliente@example.com", "name": "Ana"}],
  "template_id": "<uuid de la plantilla de la empresa>",
  "idempotency_key": "pedido-3251789819:confirmado",
  "variables": {
    "first_name": "Ana",
    "order_number": "3251789819",
    "order_url": "https://tienda.example/pedidos/3251789819",
    "status": "received",
    "currency": "S/",
    "total": "2027.70",
    "items": [
      {"name": "Lavadora 15 kg", "detail": "Color grafito", "quantity": 1,
       "price": "1899.00", "image_url": "https://tienda.example/img/lavadora.jpg",
       "delivery": "Llega el miércoles 16 de septiembre"}
    ]
  }
}
```

* **`idempotency_key`** de la forma `pedido-<id>:<evento>`: un reintento del ERP no manda un
  segundo correo al cliente.
* **Importes** como número o cadena decimal. Nunca se calculan en la plantilla: el total, los
  impuestos y los descuentos son del ERP. La plantilla solo los formatea (`money`).
* **Fechas** ya redactadas por el ERP (`"Llega el miércoles 16 de septiembre"`), porque el
  idioma y el formato son suyos.
* El ERP guarda el `template_id` como **configuración por empresa**, nunca en su código.

## 2. Lo que cambia en `templates`

| Pieza | Regla |
|---|---|
| Tipo `list` | Lista de objetos de **un solo nivel**. Declara sus campos (`fields`), cada uno de un tipo simple. Sin listas dentro de listas, sin default. Hasta `MaxListFields` (20) campos y `MaxListItems` (100) elementos por valor; más elementos es 422, no un recorte silencioso. |
| Tipo `image` | URL absoluta **solo `https`**, sin `data:` ni `.svg`: Gmail no muestra SVG ni imágenes en línea, y pasa cada imagen por su proxy, que exige una URL pública. |
| Números | Se valida la gramática de un número JSON. `NaN`, `Inf`, `0x..` o `+5` se rechazan donde entran. |
| `{{range .items}}` | Único bucle admitido, solo sobre una variable `list`. Dentro, `{{.campo}}` es un campo del elemento (comprobado al compilar) y `{{$.variable}}` una variable de la plantilla. Sin `range` anidado, sin variables locales. Admite `{{else}}` para la lista vacía. |
| Funciones nuevas | `money` (dos decimales, redondeo al alejarse de cero, separador de miles), `nonzero` (un importe opcional llega como texto y `{{if}}` lo daría por cierto con `"0"`), `count`, `take N` y `rest N` sobre listas, y las comparaciones `eq` y `ne` (solo entre una variable y un texto literal: comparar un número con un entero fallaría al enviar, no al guardar), `and`, `or`, `not`. |
| Tope visible | `{{range take 20 .items}}` muestra 20 y `{{rest 20 .items}}` dice cuántos quedan: el correo enlaza al pedido completo en vez de crecer sin límite. |
| Verificador | Regla nueva `unbounded_list` (aviso) cuando una lista se recorre sin `take`. La verificación renderiza cada lista con el máximo que puede mostrar, así `html_too_large` mide el peor caso real frente al recorte de 102 KB de Gmail. |

Lo que **no** cambia: el escapado contextual de `html/template` sigue siendo el que protege cada
valor, dentro de la lista también (un `src` recibe una URL filtrada, un texto se escapa como
HTML). No existe ni existirá un tipo de "HTML crudo". `transactional` no cambia: ya pasa las
variables como JSON sin interpretarlas.

## 3. Reglas de Gmail que la plantilla respeta

* **Recorte a 102 KB**: tope visible por lista y medición del peor caso (sección 2).
* **Imágenes**: `https`, con `width` y `alt` y alto automático (una foto no cuadrada no se deforma); nunca SVG ni `data:`. Sin imágenes de
  fondo para información: todo dato va en texto.
* **Transaccional puro**: la plantilla de pedido no lleva enlace de baja ni contenido comercial.
  Gmail y la ley separan el correo que el cliente espera del publicitario; mezclarlos lo acerca a
  la pestaña de Promociones y le quita la exención de la baja.
* **Preencabezado** propio, tipografías seguras con alternativa, ancho de 600 px, tablas para la
  maquetación (Outlook) y contraste suficiente también con el modo oscuro de Gmail.
* **Autenticación**: SPF, DKIM y DMARC alineados ya los garantiza el dominio de envío verificado.

## 4. Editor y galería

* Declaración de variables `list` con sus campos, y valores de prueba de una lista en JSON.
* Categoría de bloques **Pedido**: estado (`status`: `received`, `preparing`, `shipped`,
  `delivered`), productos (`items`, `currency`, `order_url`) y resumen de importes (`subtotal`,
  `shipping`, `discount`, `total`). Cada bloque declara sus variables al soltarlo.
* Plantillas nuevas en la galería: **Confirmación de pedido** (estado, productos, resumen, entrega
  y pago) y **Pedido en camino** (transportista, seguimiento, fecha estimada y productos). Las dos
  con pie transaccional: datos de la empresa y "ver en el navegador", sin baja.
* Ninguna acción de plantilla queda suelta entre `<table>`, `<tr>` y `<td>`: el lienzo lee el
  documento con el parser del navegador, que la sacaría de la tabla. Cada fila opcional es su
  propia tabla y cada condición va dentro de una celda.

Campos de cada producto: `name` y `quantity` requeridos, `price` requerido (importe **de la
línea**, cantidad incluida), `detail`, `image_url` y `delivery` opcionales (un opcional en blanco
cuenta como ausente).

## 5. Fases

| Fase | Contenido | Estado |
|---|---|---|
| 1 | Tipos `list` e `image`, validación de números, `range` y funciones, verificador | Hecho (2026-09-25) |
| 2 | Editor: declaración de listas, valores de prueba, bloques de pedido y plantillas de galería | Hecho (2026-09-25) |
| 3 | Referencia estable a la plantilla por clave (`pedido.confirmado`) en vez de `template_id`, para que el ERP no guarde un id por empresa | Pendiente |
| 4 | Lista de hosts de imagen permitidos por empresa en el kit de marca | Pendiente |
| 5 | Marcado de Gmail para pedidos (schema.org `Order`), que exige registrar el remitente con Google | Pendiente |
| 6 | El ERP pasa del relay SMTP a la API con plantilla (`Plan_Integracion_ERP.md`, fase C4) | En el otro repositorio |
