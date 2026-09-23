// Operaciones de texto sobre el MJML fuente y el HTML compilado. No dependen del
// compilador, asi que la pantalla del editor las usa sin cargar mjml-browser.

/** Escapa texto para meterlo en el contenido o en un atributo de MJML. */
export function escapeMjml(value: string): string {
  return value
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

function unescapeMjml(value: string): string {
  return value
    .replace(/&quot;/g, '"')
    .replace(/&gt;/g, '>')
    .replace(/&lt;/g, '<')
    .replace(/&amp;/g, '&');
}

const PREVIEW = /<mj-preview>([\s\S]*?)<\/mj-preview>/i;
const PREVIEW_ALL = /\s*<mj-preview>[\s\S]*?<\/mj-preview>/gi;
const HEAD_OPEN = /<mj-head(\s[^>]*)?>/i;
const MJML_OPEN = /<mjml(\s[^>]*)?>/i;

/** Texto de previsualizacion (preheader) guardado en el <mj-preview> del documento. */
export function readPreheader(source: string): string {
  const match = PREVIEW.exec(source);
  return match?.[1] ? unescapeMjml(match[1].trim()) : '';
}

/**
 * Fija el preheader: MJML lo emite como el div oculto al principio del cuerpo que la
 * bandeja de entrada muestra junto al asunto (regla missing_preheader). Vacio lo quita.
 */
export function writePreheader(source: string, preheader: string): string {
  const cleaned = source.replace(PREVIEW_ALL, '');
  const text = preheader.trim();
  if (!text) return cleaned;
  const tag = `<mj-preview>${escapeMjml(text)}</mj-preview>`;
  const head = HEAD_OPEN.exec(cleaned);
  if (head) {
    const at = head.index + head[0].length;
    return `${cleaned.slice(0, at)}${tag}${cleaned.slice(at)}`;
  }
  const root = MJML_OPEN.exec(cleaned);
  if (!root) return cleaned;
  const at = root.index + root[0].length;
  return `${cleaned.slice(0, at)}<mj-head>${tag}</mj-head>${cleaned.slice(at)}`;
}

const EMPTY_HEAD = /\s*<mj-head(\s[^>]*)?>\s*<\/mj-head>/gi;

/** Quita el preheader (y la cabecera que quede vacia) antes de cargar el MJML en el lienzo. */
export function stripPreheader(source: string): string {
  return source.replace(PREVIEW_ALL, '').replace(EMPTY_HEAD, '');
}

const BLOCK_TAGS = new Set([
  'P',
  'DIV',
  'TR',
  'TABLE',
  'H1',
  'H2',
  'H3',
  'H4',
  'H5',
  'H6',
  'LI',
  'UL',
  'OL',
  'BR',
  'HR',
]);

function isHidden(el: Element): boolean {
  const style = (el.getAttribute('style') ?? '').replace(/\s+/g, '').toLowerCase();
  return style.includes('display:none');
}

/**
 * Version en texto plano a partir del HTML compilado: el texto visible por bloques y cada
 * enlace con su destino entre parentesis. Sin el preheader oculto ni estilos.
 */
export function htmlToText(html: string): string {
  const doc = new DOMParser().parseFromString(html, 'text/html');
  const out: string[] = [];
  const walk = (node: Node) => {
    if (node.nodeType === Node.TEXT_NODE) {
      out.push((node.textContent ?? '').replace(/\s+/g, ' '));
      return;
    }
    if (node.nodeType !== Node.ELEMENT_NODE) return;
    const el = node as Element;
    if (el.tagName === 'STYLE' || el.tagName === 'SCRIPT' || el.tagName === 'HEAD') return;
    if (isHidden(el)) return;
    const block = BLOCK_TAGS.has(el.tagName);
    if (block) out.push('\n');
    el.childNodes.forEach(walk);
    if (el.tagName === 'A') {
      const href = el.getAttribute('href') ?? '';
      const label = (el.textContent ?? '').trim();
      if (href && !href.startsWith('#') && href !== label) out.push(` (${href})`);
    }
    if (block) out.push('\n');
  };
  walk(doc.body);
  return out
    .join('')
    .split('\n')
    .map((line) => line.trim())
    .filter((line, i, lines) => line !== '' || (i > 0 && lines[i - 1] !== ''))
    .join('\n')
    .trim();
}
