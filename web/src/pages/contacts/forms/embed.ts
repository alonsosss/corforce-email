import type { FormEmbed, FormField } from '@/api/forms';

// Fragmentos que la empresa copia en su sitio. Las URL las calcula el servicio; aqui solo se
// escapan para que un valor no pueda salirse del atributo.

/** Alto minimo del marco incrustado; el mismo que usa el bloque de las paginas. */
export const EMBED_MIN_HEIGHT_PX = 480;

function escapeAttr(value: string): string {
  return value
    .replace(/&/g, '&amp;')
    .replace(/"/g, '&quot;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;');
}

export function scriptSnippet(embed: FormEmbed): string {
  return `<script src="${escapeAttr(embed.script_url)}" async></script>`;
}

export function iframeSnippet(embed: FormEmbed, title: string): string {
  return (
    `<iframe src="${escapeAttr(embed.iframe_url)}" title="${escapeAttr(title)}" ` +
    `style="width:100%;border:0;min-height:${EMBED_MIN_HEIGHT_PX}px" loading="lazy"></iframe>`
  );
}

/**
 * Cuerpo JSON del envio para una integracion propia: el token es el de la definicion,
 * consent confirma que la persona acepto el texto y homepage es el campo trampa, que va vacio.
 */
export function submitBodyExample(fields: readonly FormField[], tokenPlaceholder: string): string {
  const values = Object.fromEntries(fields.map((f) => [f.key, '']));
  return JSON.stringify(
    { token: tokenPlaceholder, fields: values, consent: true, homepage: '' },
    null,
    2,
  );
}
