import {
  EDITOR_KIND_GRAPESJS_MJML,
  type BrandKit,
  type TemplateAsset,
  type TemplateDetail,
  type TemplateKind,
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

/** Como arranca el lienzo de una plantilla nueva: con la galeria abierta o vacio. */
export type EditorStart = 'gallery' | 'blank';

/** Alta pendiente: la plantilla se crea con su primer diseno al guardar (POST /templates). */
export interface NewTemplateDraft {
  name: string;
  description: string;
  kind: TemplateKind;
  start: EditorStart;
}

export type EditorTarget =
  { mode: 'existing'; template: TemplateDetail } | { mode: 'new'; draft: NewTemplateDraft };

/** Lo que viaja en el estado de la navegacion hacia el editor. */
export interface EditorLocationState {
  draft?: NewTemplateDraft;
  /** Version recien creada cuyo envio de prueba se abre al llegar. */
  testSend?: number;
}

export interface EditorData {
  target: EditorTarget;
  meta: TemplatesMeta;
  /** Version de partida: el borrador mas reciente o, si no hay, la publicada. */
  base: TemplateVersion | null;
  brand: BrandContext;
}

/** Datos de la plantilla que el editor muestra, exista ya o este por crearse. */
export function targetInfo(target: EditorTarget): {
  id: string | null;
  name: string;
  kind: TemplateKind;
  archived: boolean;
  start: EditorStart;
} {
  if (target.mode === 'existing') {
    const { template } = target;
    return {
      id: template.id,
      name: template.name,
      kind: template.kind,
      archived: template.status === 'archived',
      start: 'gallery',
    };
  }
  const { draft } = target;
  return { id: null, name: draft.name, kind: draft.kind, archived: false, start: draft.start };
}

/** Lee el alta pendiente del estado de la navegacion; null si falta o esta mal formada. */
export function readNewDraft(
  state: unknown,
  kinds: readonly TemplateKind[],
): NewTemplateDraft | null {
  const draft = (state as EditorLocationState | null)?.draft;
  if (typeof draft !== 'object' || draft === null) return null;
  const { name, description, kind, start } = draft;
  if (typeof name !== 'string' || !name.trim() || typeof description !== 'string') return null;
  if (!kinds.includes(kind) || (start !== 'gallery' && start !== 'blank')) return null;
  return { name, description, kind, start };
}

/** Version cuyo envio de prueba pide abrir la navegacion, o null. */
export function readTestSendRequest(state: unknown): number | null {
  const value = (state as EditorLocationState | null)?.testSend;
  return typeof value === 'number' && Number.isInteger(value) && value > 0 ? value : null;
}

/** Numero de la version que se abre: el borrador mas reciente, la publicada o la ultima. */
export function baseVersionNumber(
  template: Pick<TemplateDetail, 'versions' | 'current_version'>,
): number | null {
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
