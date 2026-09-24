/*
 * HTML del editor de redaccion. Lo que entra en el editor (pegado, firma, borrador) pasa
 * antes por una lista blanca: el editor vive en el DOM de la aplicacion, asi que ese HTML
 * nunca se inserta tal cual. Se lee con DOMParser (documento inerte: no ejecuta ni carga
 * nada) y se reconstruye nodo a nodo con solo formato basico. El servicio vuelve a sanear
 * al enviar (HTMLSanitizer.Outgoing).
 */

const INLINE_TAGS = new Set(['B', 'STRONG', 'I', 'EM', 'U']);
const BLOCK_TAGS = new Set(['P', 'DIV', 'UL', 'OL', 'LI', 'BLOCKQUOTE']);
// Se descartan con todo su contenido: no es texto que el usuario haya querido pegar.
const DROPPED_TAGS = new Set([
  'SCRIPT',
  'STYLE',
  'HEAD',
  'TITLE',
  'META',
  'LINK',
  'TEMPLATE',
  'NOSCRIPT',
  'IFRAME',
  'OBJECT',
  'EMBED',
  'SVG',
  'MATH',
  'FORM',
  'INPUT',
  'BUTTON',
  'SELECT',
  'TEXTAREA',
]);
// Bloques que no se conservan pero separan lineas: pasan a DIV.
const BLOCKISH_TAGS = new Set([
  'H1',
  'H2',
  'H3',
  'H4',
  'H5',
  'H6',
  'SECTION',
  'ARTICLE',
  'HEADER',
  'FOOTER',
  'TABLE',
  'TR',
  'PRE',
  'ADDRESS',
  'FIGURE',
]);
const SAFE_LINK = /^(https?:|mailto:)/i;
const SAFE_IMAGE = /^(https:|data:image\/(png|jpeg|gif|webp);base64,)/i;

export interface CleanOptions {
  /** Conserva imagenes https o incrustadas (la firma propia); el pegado no las trae. */
  images?: boolean;
}

function copyChildren(from: Node, to: Node, doc: Document, options: CleanOptions): void {
  from.childNodes.forEach((child) => {
    const cleaned = cleanNode(child, doc, options);
    if (cleaned) to.appendChild(cleaned);
  });
}

function cleanNode(node: Node, doc: Document, options: CleanOptions): Node | null {
  if (node.nodeType === Node.TEXT_NODE) return doc.createTextNode(node.textContent ?? '');
  if (node.nodeType !== Node.ELEMENT_NODE) return null;
  const element = node as Element;
  const tag = element.tagName.toUpperCase();
  if (DROPPED_TAGS.has(tag)) return null;
  if (tag === 'BR') return doc.createElement('br');
  if (tag === 'IMG') {
    const src = element.getAttribute('src')?.trim() ?? '';
    if (!options.images || !SAFE_IMAGE.test(src)) return null;
    const img = doc.createElement('img');
    img.setAttribute('src', src);
    const alt = element.getAttribute('alt');
    if (alt) img.setAttribute('alt', alt);
    for (const size of ['width', 'height']) {
      const value = element.getAttribute(size);
      if (value && /^\d{1,4}$/.test(value)) img.setAttribute(size, value);
    }
    return img;
  }
  if (tag === 'A') {
    const href = element.getAttribute('href')?.trim() ?? '';
    if (!SAFE_LINK.test(href)) {
      const fragment = doc.createDocumentFragment();
      copyChildren(element, fragment, doc, options);
      return fragment;
    }
    const link = doc.createElement('a');
    link.setAttribute('href', href);
    copyChildren(element, link, doc, options);
    return link;
  }
  if (INLINE_TAGS.has(tag) || BLOCK_TAGS.has(tag)) {
    const copy = doc.createElement(tag.toLowerCase());
    copyChildren(element, copy, doc, options);
    return copy;
  }
  if (BLOCKISH_TAGS.has(tag)) {
    const block = doc.createElement('div');
    copyChildren(element, block, doc, options);
    return block;
  }
  const fragment = doc.createDocumentFragment();
  copyChildren(element, fragment, doc, options);
  return fragment;
}

/**
 * Nodos con solo formato basico (negrita, cursiva, subrayado, listas, citas y enlaces),
 * creados en `doc` para insertarlos sin pasar por innerHTML.
 */
export function cleanFragment(
  html: string,
  options: CleanOptions = {},
  doc: Document = document,
): DocumentFragment {
  const source = new DOMParser().parseFromString(html, 'text/html');
  const fragment = doc.createDocumentFragment();
  copyChildren(source.body, fragment, doc, options);
  return fragment;
}

/** Lo mismo como texto HTML, construido en un documento aparte que no carga nada. */
export function cleanHtml(html: string, options: CleanOptions = {}): string {
  const inert = document.implementation.createHTMLDocument('');
  const container = inert.createElement('div');
  container.appendChild(cleanFragment(html, options, inert));
  return container.innerHTML;
}

function escapeHtml(text: string): string {
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

function linesToHtml(lines: readonly string[]): string {
  return lines.map((line) => `<div>${line ? escapeHtml(line) : '<br>'}</div>`).join('');
}

/** Texto pegado a HTML, linea a linea y escapado. */
export function plainToHtml(text: string): string {
  return text ? linesToHtml(text.split(/\r?\n/)) : '';
}

/**
 * Texto a HTML del editor: cada linea en su bloque y las lineas citadas con "> " dentro de
 * una cita, para que responder con formato conserve la cita del original.
 */
export function textToHtml(text: string): string {
  if (!text) return '';
  const lines = text.split(/\r?\n/);
  let out = '';
  let quoted: string[] = [];
  let plain: string[] = [];
  const flushQuoted = () => {
    if (quoted.length) out += `<blockquote>${linesToHtml(quoted)}</blockquote>`;
    quoted = [];
  };
  const flushPlain = () => {
    out += linesToHtml(plain);
    plain = [];
  };
  for (const line of lines) {
    if (line.startsWith('>')) {
      flushPlain();
      quoted.push(line.replace(/^> ?/, ''));
    } else {
      flushQuoted();
      plain.push(line);
    }
  }
  flushQuoted();
  flushPlain();
  return out;
}

/** Separador de firma habitual (RFC 3676, 4.3): los clientes la reconocen y la pliegan. */
export const SIGNATURE_DELIMITER = '-- ';

export function signatureText(text: string): string {
  return `\n\n${SIGNATURE_DELIMITER}\n${text}`;
}

export function signatureHtml(html: string): string {
  return `<div><br></div><div>${SIGNATURE_DELIMITER}<br>${cleanHtml(html, { images: true })}</div>`;
}

/** Hay algo escrito en el editor, aunque sea solo una imagen. */
export function htmlHasContent(html: string): boolean {
  if (!html) return false;
  const doc = new DOMParser().parseFromString(html, 'text/html');
  return (doc.body.textContent ?? '').trim() !== '' || doc.body.querySelector('img') !== null;
}
