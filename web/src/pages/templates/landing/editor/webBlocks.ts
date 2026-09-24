import type { MessageKey } from '@/i18n';
import { EMBED_MIN_HEIGHT_PX } from '@/pages/contacts/forms/embed';
import { defaultHref, IMAGE_PLACEHOLDER } from '../../editor/blocks';
import type { BrandTokens } from '../../editor/brand';

// Bloques del editor de paginas en HTML y CSS en linea, sin MJML ni codigo: el servicio sanea
// lo que se guarda (sin scripts, formularios ni marcos). Son funciones puras del kit de marca
// para que las pruebas los usen sin cargar el lienzo.

export type WebBlockId =
  | 'section'
  | 'columns-2'
  | 'columns-3'
  | 'heading'
  | 'text'
  | 'image'
  | 'button'
  | 'divider'
  | 'spacer';

export type WebBlockCategory = 'layout' | 'content';

export interface WebBlockDefinition {
  id: WebBlockId;
  label: MessageKey;
  category: WebBlockCategory;
  /** Abre el gestor de imagenes al soltarlo. */
  activate?: boolean;
  content: (brand: BrandTokens) => string;
}

/** Atributo que el servicio sustituye por el marco del formulario al servir la pagina. */
export const FORM_MARKER_ATTRIBUTE = 'data-cf-form';
export const FORM_HEIGHT_ATTRIBUTE = 'data-cf-height';

const CONTENT_WIDTH = '960px';

export function escapeHtml(value: string): string {
  return value
    .replace(/&/g, '&amp;')
    .replace(/"/g, '&quot;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;');
}

function text(brand: BrandTokens, body: string): string {
  return (
    `<p style="margin:0 0 16px;font-family:${escapeHtml(brand.fontFamily)};font-size:17px;` +
    `line-height:1.6;color:${brand.text};">${body}</p>`
  );
}

function heading(brand: BrandTokens, body: string): string {
  return (
    `<h2 style="margin:0 0 16px;font-family:${escapeHtml(brand.fontFamily)};font-size:32px;` +
    `line-height:1.25;font-weight:700;color:${brand.text};">${body}</h2>`
  );
}

function columns(brand: BrandTokens, count: number): string {
  const cells = Array.from(
    { length: count },
    (_, i) =>
      `<div style="flex:1 1 240px;min-width:0;">` +
      text(brand, `Columna ${i + 1}. Escribe aquí el contenido.`) +
      `</div>`,
  );
  return (
    `<div style="display:flex;flex-wrap:wrap;gap:32px;max-width:${CONTENT_WIDTH};` +
    `margin:0 auto;padding:32px 24px;">${cells.join('')}</div>`
  );
}

export function webButton(brand: BrandTokens, label: string, href: string): string {
  return (
    `<a href="${escapeHtml(href)}" style="display:inline-block;padding:14px 28px;` +
    `background-color:${brand.primary};color:#FFFFFF;border-radius:6px;text-decoration:none;` +
    `font-family:${escapeHtml(brand.fontFamily)};font-size:16px;font-weight:600;">` +
    `${escapeHtml(label)}</a>`
  );
}

/**
 * Marcador del formulario de suscripcion: el servicio lo cambia por el iframe del formulario
 * de la clave indicada. En el lienzo se ve como un recuadro con el nombre del formulario.
 */
export function formMarker(key: string, height = EMBED_MIN_HEIGHT_PX): string {
  return `<div ${FORM_MARKER_ATTRIBUTE}="${escapeHtml(key)}" ${FORM_HEIGHT_ATTRIBUTE}="${height}"></div>`;
}

export function imageTag(src: string, alt: string): string {
  return (
    `<img src="${escapeHtml(src)}" alt="${escapeHtml(alt)}" ` +
    `style="display:block;max-width:100%;height:auto;margin:0 auto 16px;" />`
  );
}

export const WEB_BLOCKS: readonly WebBlockDefinition[] = [
  {
    id: 'section',
    label: 'templates.editor.block.section',
    category: 'layout',
    content: (b) =>
      `<section style="padding:48px 24px;background-color:${b.surface};">` +
      `<div style="max-width:${CONTENT_WIDTH};margin:0 auto;">` +
      heading(b, 'Título de la sección') +
      text(b, 'Escribe aquí el contenido de la sección.') +
      `</div></section>`,
  },
  {
    id: 'columns-2',
    label: 'templates.editor.block.columns2',
    category: 'layout',
    content: (b) => columns(b, 2),
  },
  {
    id: 'columns-3',
    label: 'templates.editor.block.columns3',
    category: 'layout',
    content: (b) => columns(b, 3),
  },
  {
    id: 'heading',
    label: 'templates.editor.block.heading',
    category: 'content',
    content: (b) => heading(b, 'Título de la página'),
  },
  {
    id: 'text',
    label: 'templates.editor.block.text',
    category: 'content',
    content: (b) => text(b, 'Escribe aquí el texto de la página.'),
  },
  {
    id: 'image',
    label: 'templates.editor.block.image',
    category: 'content',
    activate: true,
    content: () => imageTag(IMAGE_PLACEHOLDER, ''),
  },
  {
    id: 'button',
    label: 'templates.editor.block.button',
    category: 'content',
    content: (b) =>
      `<div style="margin:0 0 16px;">${webButton(b, 'Ver más', defaultHref(b))}</div>`,
  },
  {
    id: 'divider',
    label: 'templates.editor.block.divider',
    category: 'content',
    content: (b) => `<hr style="border:0;border-top:1px solid ${b.border};margin:24px 0;" />`,
  },
  {
    id: 'spacer',
    label: 'templates.editor.block.spacer',
    category: 'content',
    content: () => `<div style="height:32px;"></div>`,
  },
];

export function webBlockById(id: string): WebBlockDefinition | undefined {
  return WEB_BLOCKS.find((b) => b.id === id);
}
