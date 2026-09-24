import {
  EDITOR_KIND_GRAPESJS_WEB,
  type PageContent,
  type PageEditor,
  type PageDetail,
  type PagesMeta,
  type PageVersion,
} from '@/api/pages';
import { t } from '@/i18n';
import { IMAGE_PLACEHOLDER } from '../../editor/blocks';
import { baseVersionNumber } from '../../editor/session';

// Reglas de la sesion de edicion de una pagina que no dependen del lienzo.

export interface PageDocument {
  title: string;
  description: string;
}

export interface PageDocumentErrors {
  title?: string;
  description?: string;
  /** El contenido del lienzo no se puede guardar (tamano, imagenes sin elegir). */
  content?: string;
}

/** Lo que el lienzo devuelve al guardar. */
export interface CanvasOutput {
  html: string;
  css: string;
  project: Record<string, unknown>;
}

const encoder = new TextEncoder();

export function utf8Bytes(value: string): number {
  return encoder.encode(value).length;
}

/**
 * Cuerpo de POST /templates/pages/{id}/versions. Los topes son los de GET
 * /templates/pages/meta; el servicio vuelve a comprobarlos y sanea el HTML y el CSS.
 */
export function buildPageContent(
  doc: PageDocument,
  canvas: CanvasOutput,
  meta: Pick<
    PagesMeta,
    | 'max_title_length'
    | 'max_description_length'
    | 'max_html_bytes'
    | 'max_css_bytes'
    | 'max_editor_bytes'
  >,
): { content: PageContent | null; errors: PageDocumentErrors } {
  const errors: PageDocumentErrors = {};
  const title = doc.title.trim();
  const description = doc.description.trim();
  if (!title) errors.title = t('validation.required');
  else if (title.length > meta.max_title_length) {
    errors.title = t('validation.maxLength', { n: meta.max_title_length });
  }
  if (description.length > meta.max_description_length) {
    errors.description = t('validation.maxLength', { n: meta.max_description_length });
  }

  const editor: PageEditor = { kind: EDITOR_KIND_GRAPESJS_WEB, project: canvas.project };
  if (canvas.html.includes(IMAGE_PLACEHOLDER)) {
    errors.content = t('templates.pages.editor.imagePending');
  } else if (utf8Bytes(canvas.html) > meta.max_html_bytes) {
    errors.content = t('templates.pages.editor.htmlTooLarge', { n: meta.max_html_bytes });
  } else if (utf8Bytes(canvas.css) > meta.max_css_bytes) {
    errors.content = t('templates.pages.editor.cssTooLarge', { n: meta.max_css_bytes });
  } else if (utf8Bytes(JSON.stringify(editor)) > meta.max_editor_bytes) {
    errors.content = t('templates.pages.editor.tooLarge', { n: meta.max_editor_bytes });
  }

  if (Object.values(errors).some(Boolean)) return { content: null, errors };
  return {
    content: { title, description, html: canvas.html, css: canvas.css, editor },
    errors,
  };
}

/** Documento con el que arranca el editor; sin version, el titulo es el nombre de la pagina. */
export function initialPageDocument(version: PageVersion | null, pageName: string): PageDocument {
  return version
    ? { title: version.title, description: version.description }
    : { title: pageName, description: '' };
}

/** Version que se abre, con la misma regla que el editor de plantillas. */
export function basePageVersion(detail: PageDetail): number | null {
  return baseVersionNumber({
    versions: detail.versions,
    current_version: detail.page.current_version,
  });
}

/**
 * Documento de una version para verla en un marco aislado: el CSS de la pagina en su hoja y
 * el HTML ya saneado por el servicio. Un cierre de style dentro del CSS no rompe la hoja.
 */
export function previewDocument(version: Pick<PageVersion, 'title' | 'html' | 'css'>): string {
  const css = version.css.replace(/<\/style/gi, '<\\/style');
  const title = escapeText(version.title);
  return (
    `<!DOCTYPE html><html><head><meta charset="utf-8"><title>${title}</title>` +
    `<style>${css}</style></head><body>${version.html}</body></html>`
  );
}

function escapeText(value: string): string {
  return value.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

/** Borrador hecho con este editor: se puede publicar tal cual si no hay cambios. */
export function savedPageDraft(version: PageVersion | null): number | null {
  if (!version || version.status !== 'draft') return null;
  return version.editor?.kind === EDITOR_KIND_GRAPESJS_WEB ? version.version : null;
}
