/**
 * Historial citado de un mensaje recibido: la cadena de respuestas anteriores que casi todos
 * los clientes pegan al final. El lector lo pliega, como Gmail, para que se vea solo lo nuevo.
 * Desde la marca de cita del cliente se pliega todo lo que sigue; sin marca, solo un
 * blockquote final. Un mensaje que es todo cita se muestra entero.
 */
export interface QuotedSplit {
  main: string;
  quoted: string;
}

/**
 * Marcas de cita de los clientes mas comunes: Gmail, Apple Mail y Thunderbird
 * (blockquote type=cite), Thunderbird (moz-cite-prefix), Outlook (divRplyFwdMsg,
 * appendonsend) y Yahoo.
 */
const CLIENT_QUOTE_SELECTOR = [
  '.gmail_quote_container',
  '.gmail_quote',
  '.moz-cite-prefix',
  'blockquote[type="cite"]',
  '#appendonsend',
  '#divRplyFwdMsg',
  '.yahoo_quoted',
].join(',');

/** Linea de atribucion ("El ... escribio:", "On ... wrote:"): termina en dos puntos. */
const ATTRIBUTION = /:\s*$/;
/** Separador de reenvio o respuesta de Outlook y otros clientes. */
const ORIGINAL_SEPARATOR =
  /^\s*-{2,}\s*(original message|mensaje original|forwarded message|mensaje reenviado)\s*-{2,}\s*$/i;

function hasContent(node: ParentNode): boolean {
  const text = node.textContent?.trim() ?? '';
  return text !== '' || node.querySelector('img') !== null;
}

/** Elemento en el que empieza la cita del final, si la hay. */
function quoteStart(body: HTMLElement): Element | null {
  const marked = body.querySelector(CLIENT_QUOTE_SELECTOR);
  if (marked) {
    const previous = marked.previousElementSibling;
    // La atribucion de Apple Mail va justo antes del blockquote; el separador de Outlook es un <hr>.
    if (previous?.tagName === 'HR') return previous;
    if (
      marked.tagName === 'BLOCKQUOTE' &&
      previous &&
      ATTRIBUTION.test(previous.textContent ?? '')
    ) {
      return previous;
    }
    return marked;
  }
  // Sin marca de cliente (el propio webmail, otros): el ultimo blockquote del nivel superior.
  const children = Array.from(body.children);
  for (let i = children.length - 1; i >= 0; i -= 1) {
    const child = children[i]!;
    if (child.tagName === 'BLOCKQUOTE') {
      const previous = child.previousElementSibling;
      return previous && ATTRIBUTION.test(previous.textContent ?? '') ? previous : child;
    }
    if (hasContent(child)) return null;
  }
  return null;
}

/**
 * Separa el HTML (ya saneado por el servicio) en lo nuevo y el historial citado. DOMParser
 * crea un documento inerte: no ejecuta nada ni carga recursos. null si no hay nada que plegar.
 */
export function splitQuotedHtml(html: string): QuotedSplit | null {
  if (!html.trim()) return null;
  const doc = new DOMParser().parseFromString(html, 'text/html');
  const body = doc.body;
  const start = quoteStart(body);
  if (!start) return null;

  const range = doc.createRange();
  range.setStartBefore(start);
  range.setEndAfter(body.lastChild ?? start);
  const quotedFragment = range.extractContents();
  if (!hasContent(body) || !hasContent(quotedFragment)) return null;

  const holder = doc.createElement('div');
  holder.appendChild(quotedFragment);
  // Lo que queda fuera del body (estilos del <head>) sigue aplicando a lo nuevo.
  const head = doc.head.innerHTML;
  return { main: `${head}${body.innerHTML}`, quoted: holder.innerHTML };
}

/**
 * Lo mismo para texto: el bloque final de lineas con ">" (con su atribucion delante) o lo que
 * sigue a un separador de mensaje original.
 */
export function splitQuotedText(text: string): QuotedSplit | null {
  const lines = text.split(/\r?\n/);
  let start = -1;

  const separator = lines.findIndex((line) => ORIGINAL_SEPARATOR.test(line));
  if (separator >= 0) {
    start = separator;
  } else {
    let end = lines.length - 1;
    while (end >= 0 && lines[end]!.trim() === '') end -= 1;
    let first = -1;
    for (let i = end; i >= 0; i -= 1) {
      const line = lines[i]!;
      if (line.startsWith('>')) first = i;
      else if (line.trim() !== '' || first === -1) break;
    }
    if (first === -1) return null;
    start = first;
    let previous = start - 1;
    while (previous >= 0 && lines[previous]!.trim() === '') previous -= 1;
    if (previous >= 0 && ATTRIBUTION.test(lines[previous]!)) start = previous;
  }

  const main = lines.slice(0, start).join('\n').trimEnd();
  const quoted = lines.slice(start).join('\n').trim();
  return main.trim() && quoted ? { main, quoted } : null;
}
