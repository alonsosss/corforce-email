import {
  EDITOR_KIND_GRAPESJS_MJML,
  type BrandKit,
  type TemplateAsset,
  type TemplateDetail,
  type TemplatesMeta,
  type TemplateVariable,
  type TemplateVersion,
} from '@/api/templates';
import { contentFromVersion } from '../content';
import { variablesToDrafts, type VariableDraft } from '../variables';
import type { BrandTokens } from './brand';
import type { DocumentDraft } from './DocumentPanel';
import { readPreheader, stripPreheader } from './mjmlSource';

// Reglas de la sesion de edicion que no dependen del lienzo: que version se abre, cual se
// puede reescribir y como se combina lo que trae una plantilla de la galeria.

export interface BrandContext {
  kit: BrandKit | null;
  logo: TemplateAsset | null;
  /** Por que no hay kit en el editor (sin permiso o el servicio no respondio). */
  unavailable: string | null;
}

export interface EditorData {
  template: TemplateDetail;
  meta: TemplatesMeta;
  /** Version de partida: el borrador mas reciente o, si no hay, la publicada. */
  base: TemplateVersion | null;
  brand: BrandContext;
}

/** Numero de la version que se abre: el borrador mas reciente, la publicada o la ultima. */
export function baseVersionNumber(template: TemplateDetail): number | null {
  const versions = [...template.versions].sort((a, b) => b.version - a.version);
  const latest = versions[0];
  if (!latest) return null;
  if (latest.status === 'draft') return latest.version;
  return template.current_version > 0 ? template.current_version : latest.version;
}

/**
 * Borrador del editor que se puede publicar tal cual si no hay cambios: uno disenado con el
 * editor. Guardar siempre crea una version nueva (el servicio no reescribe versiones), asi
 * que un borrador escrito a mano nunca se pisa.
 */
export function savedDraft(version: TemplateVersion | null): number | null {
  if (!version || version.status !== 'draft') return null;
  return version.editor?.kind === EDITOR_KIND_GRAPESJS_MJML ? version.version : null;
}

/** Documento con el que arranca el editor a partir de la version de partida. */
export function initialDocument(version: TemplateVersion | null): DocumentDraft {
  const content = contentFromVersion(version);
  return {
    subject: content.subject,
    text: content.text,
    variables: content.variables,
    preheader: version?.editor ? readPreheader(version.editor.mjml) : '',
  };
}

/** Lienzo vacio con el fondo y el ancho estandar de 600 px. */
export function emptyMjml(brand: BrandTokens): string {
  return `<mjml><mj-body background-color="${brand.background}" width="600px"></mj-body></mjml>`;
}

/** Anade las variables que usa una plantilla de la galeria y aun no estan declaradas. */
export function mergeVariables(
  drafts: VariableDraft[],
  variables: readonly TemplateVariable[],
): VariableDraft[] {
  const names = new Set(drafts.map((d) => d.name));
  const missing = variables.filter((v) => !names.has(v.name));
  return [...drafts, ...variablesToDrafts(missing)];
}

/** Aplica una plantilla de la galeria al documento: el asunto solo si estaba vacio. */
export function applyGallery(
  doc: DocumentDraft,
  mjml: string,
  subject: string,
  variables: readonly TemplateVariable[],
): { doc: DocumentDraft; canvas: string } {
  return {
    canvas: stripPreheader(mjml),
    doc: {
      ...doc,
      subject: doc.subject.trim() ? doc.subject : subject,
      preheader: readPreheader(mjml),
      variables: mergeVariables(doc.variables, variables),
    },
  };
}
