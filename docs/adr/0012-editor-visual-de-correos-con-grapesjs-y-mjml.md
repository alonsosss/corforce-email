# ADR 0012: Editor visual de correos con GrapesJS y MJML, y verificador de entregabilidad

## Estado

Aceptado (2026-09-23). Plan y registro de estado en `docs/Plan_Editor_Correos.md`.

## Contexto

Las plantillas se escriben hoy en HTML a mano, en un cuadro de texto. El producto necesita un editor
visual comparable al de las plataformas de email marketing (arrastrar y soltar, estilos, imagenes, kit
de marca, plantillas prediseñadas) que ademas impida enviar lo que los proveedores castigan: un correo
que Gmail recorta, que es solo una imagen, sin baja o sin direccion fisica, con enlaces enganosos.

Un correo no admite maquetacion libre: Gmail, Outlook (motor de Word) y Apple Mail no respetan posiciones
absolutas ni capas, y la salida facil de un editor de lienzo libre -el correo entero como una imagen- es de
lo que mas penalizan los filtros. El editor tiene que componer con secciones, columnas y bloques y producir
HTML en tablas, con estilos en linea y adaptable al movil.

## Decision

1. **Editor: GrapesJS** (licencia BSD-3) con el complemento **grapesjs-mjml** (MIT) y el compilador
   `mjml-browser` (MIT). MJML es el estandar del sector para HTML de correo compatible y adaptable; GrapesJS
   da la experiencia de lienzo (arrastrar, panel de estilos, capas, deshacer) en la aplicacion React
   existente. Se compila en el navegador; el servidor recibe el HTML ya compilado.
2. **La fuente de verdad del envio sigue siendo el HTML** de la version, como hoy: el renderizado, las
   variables, las prohibiciones (script, iframe, formularios, `javascript:`) y el enlace de baja no cambian.
   El documento del editor (proyecto de GrapesJS y MJML) se guarda junto a la version solo para volver a
   editarla; nunca se envia ni se interpreta en el servidor.
3. **Verificador de entregabilidad en `templates`**, sobre el HTML renderizado con valores de ejemplo, con
   reglas que bloquean (errores) o avisan. Publicar una version de marketing con errores se rechaza. La
   puntuacion antispam la da el Rspamd de la celda por `mail-security` (`/checkv2`), que ya lo administra.
4. **Imagenes** en el almacenamiento de objetos (`pkg/objectstore`, MinIO en el servidor propio), con clave
   por contenido (`public/<empresa>/templates/<sha256>.<ext>`), analizadas con ClamAV antes de guardarse, y
   servidas por el gateway leyendo del almacen (sin exponer MinIO) con cache larga e inmutable: un correo
   enviado sigue mostrando sus imagenes aunque la plantilla cambie.
5. **Kit de marca por empresa** en `templates` (logo, colores, tipografias, datos del pie legal). El pie con
   la direccion fisica sale de el y el verificador lo exige en marketing.

## Alternativas descartadas

* **Editores comerciales incrustados (Beefree, Unlayer):** mejor acabado inicial, pero cuota mensual,
  dependencia de un tercero para una pieza central y scripts de su dominio que la CSP estricta (nonce y
  `strict-dynamic`) obliga a abrir.
* **Editor de bloques propio (EmailBuilder.js):** licencia MIT y modelo sencillo, pero sin la experiencia de
  lienzo (seleccion directa, panel de estilos, capas) que se pide.
* **Compilar MJML en el servidor:** exige Node en un servicio Go o un servicio nuevo solo para eso. El
  servidor no necesita MJML: valida y verifica el HTML, que es lo que se envia.
* **Servir las imagenes desde MinIO publico o con redireccion a URL prefirmada:** las firmas caducan y un
  correo abierto meses despues perderia sus imagenes; un MinIO publico es otra superficie expuesta.

## Consecuencias

* Dependencias nuevas en `web/`: `grapesjs`, `grapesjs-mjml`, `mjml-browser` (y el complemento de edicion de
  imagenes que se elija con licencia MIT, BSD o Apache). Se comprueban contra la CSP en un navegador real.
* Infraestructura nueva: MinIO en `docker-compose.selfhosted.yml`, solo en la red interna, con sus claves en el
  almacen de secretos y su volumen en los respaldos.
* `templates` pasa a depender de ClamAV (analisis de imagenes) y de `mail-security` (puntuacion antispam).
  Sin ClamAV no se admiten subidas; sin `mail-security` la verificacion se hace sin puntuacion y lo dice.
