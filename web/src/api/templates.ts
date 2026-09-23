import { api } from './client';
import { endpoints } from './endpoints';
import { ERROR_CODES, isApiError } from './errors';
import { fetchList, fetchPage } from './paging';
import { cachedResource } from './resource';
import type { Page, PageQuery } from './types';

// DTOs de services/templates/internal/adapters/http/handler.go (templateResponse,
// versionResponse, versionSummaryResponse, renderedResponse, metaResponse) y
// domain/variables.go. Los valores admitidos y los topes llegan en GET /templates/meta.

export type TemplateKind = 'transactional' | 'marketing';
export type TemplateStatus = 'active' | 'archived';
export type VersionStatus = 'draft' | 'published' | 'superseded';
export type VariableType = 'string' | 'number' | 'boolean' | 'url' | 'email';

/** Valor por defecto de una variable: el backend acepta el JSON que coincide con su tipo. */
export type VariableValue = string | number | boolean;

export interface TemplateVariable {
  name: string;
  type: VariableType;
  required: boolean;
  default?: VariableValue;
}

export interface Template {
  id: string;
  name: string;
  description: string;
  kind: TemplateKind;
  status: TemplateStatus;
  /** 0 mientras no hay ninguna version publicada. */
  current_version: number;
  created_by: string;
  created_at: string;
  updated_at: string;
}

/** Unico formato de diseno guardado por ahora (docs/Plan_Editor_Correos.md, 3.1). */
export const EDITOR_KIND_GRAPESJS_MJML = 'grapesjs-mjml';
export type EditorKind = typeof EDITOR_KIND_GRAPESJS_MJML;

/**
 * Documento del editor visual guardado junto a la version, solo para volver a editarla.
 * `project` es el getProjectData() de GrapesJS, opaco para el servidor; lo que se envia
 * sigue siendo el `html` de la version.
 */
export interface EditorDocument {
  kind: EditorKind;
  project: Record<string, unknown>;
  mjml: string;
}

export interface TemplateVersion {
  id: string;
  template_id: string;
  version: number;
  subject: string;
  html: string;
  text: string | null;
  variables: TemplateVariable[] | null;
  /** null o ausente: HTML escrito a mano. */
  editor?: EditorDocument | null;
  status: VersionStatus;
  published_at: string | null;
  created_by: string;
  created_at: string;
}

export interface VersionSummary {
  id: string;
  version: number;
  status: VersionStatus;
  published_at: string | null;
  created_by: string;
  created_at: string;
}

export interface TemplateDetail extends Template {
  current: TemplateVersion | null;
  versions: VersionSummary[];
}

export interface TemplateContent {
  subject: string;
  html: string;
  text?: string;
  variables: TemplateVariable[];
  editor?: EditorDocument | null;
}

export interface CreateTemplateRequest extends TemplateContent {
  name: string;
  description: string;
  kind: TemplateKind;
}

export interface UpdateTemplateRequest {
  name?: string;
  description?: string;
  status?: TemplateStatus;
}

export interface TemplateListQuery extends PageQuery {
  kind?: TemplateKind;
  status?: TemplateStatus;
  search?: string;
}

export interface RenderRequest {
  version?: number;
  variables: Record<string, VariableValue>;
}

export interface RenderedTemplate {
  subject: string;
  html: string;
  text: string;
  version: number;
}

export interface ReservedVariable {
  name: string;
  type: VariableType;
}

export interface TemplateLimits {
  max_name_length: number;
  max_description_length: number;
  max_variables: number;
  max_subject_bytes: number;
  max_html_bytes: number;
  /** Tope del documento del editor serializado (3.1). */
  max_editor_bytes: number;
  max_brand_colors: number;
  max_brand_fonts: number;
  max_asset_bytes: number;
  max_asset_dimension: number;
  /** Destinatarios de un envio de prueba. */
  max_test_recipients: number;
}

/** Tipografia admitida en el kit, con su pila de alternativas seguras para correo. */
export interface BrandFont {
  name: string;
  stack: string;
  /** Fuente web: solo la cargan algunos clientes; el resto usa la pila. */
  web: boolean;
}

/** GET /templates/meta: valores de domain/entities.go y domain/variables.go. */
export interface TemplatesMeta {
  kinds: TemplateKind[];
  statuses: TemplateStatus[];
  version_statuses: VersionStatus[];
  variable_types: VariableType[];
  reserved_variables: ReservedVariable[];
  editor_kinds: EditorKind[];
  /** Lista cerrada de tipografias del kit de marca. */
  brand_fonts: BrandFont[];
  /** Formatos de imagen que admite POST /assets (se comprueban por la firma del fichero). */
  asset_content_types: string[];
  limits: TemplateLimits;
}

/** Datos del pie legal del kit de marca; la direccion la exige publicar marketing. */
export interface BrandKitFooter {
  company: string;
  address: string;
  website: string;
  support_email: string;
}

/** GET /templates/brand-kit. Sin kit guardado llega vacio y con updated_at null. */
export interface BrandKit {
  logo_asset_id: string | null;
  colors: string[];
  fonts: string[];
  footer: BrandKitFooter;
  updated_at: string | null;
}

export type BrandKitInput = Omit<BrandKit, 'updated_at'>;

/** Imagen de la empresa: url absoluta, servida por el gateway con cache inmutable. */
export interface TemplateAsset {
  id: string;
  url: string;
  content_type: string;
  size_bytes: number;
  width: number;
  height: number;
  name: string;
  created_at: string;
}

export interface AssetPage {
  items: TemplateAsset[];
  /** null: no hay mas paginas. */
  nextCursor: string | null;
}

export interface AssetListQuery {
  limit?: number;
  cursor?: string | null;
}

export type CheckSeverity = 'error' | 'warning';

export interface CheckIssue {
  code: string;
  severity: CheckSeverity;
  message: string;
  count?: number;
}

export interface CheckStats {
  html_bytes: number;
  text_chars: number;
  images: number;
  links: number;
  text_image_ratio: number;
}

export interface SpamSymbol {
  name: string;
  score: number;
  description: string;
}

/** Puntuacion de Rspamd por mail-security; available=false si no respondio. */
export interface SpamResult {
  available: boolean;
  score?: number;
  required?: number;
  action?: string;
  symbols?: SpamSymbol[];
}

/** POST /templates/check y POST /templates/{id}/versions/{v}/check. */
export interface CheckResult {
  passed: boolean;
  issues: CheckIssue[];
  stats: CheckStats;
  spam: SpamResult;
}

/** POST /templates/{id}/versions/{v}/test-send: la prueba sale por transactional. */
export interface TestSendRequest {
  from: { email: string; name?: string };
  to: string[];
  variables?: Record<string, VariableValue>;
}

export interface TestSendResult {
  messages: { id: string; status: string; email: string }[];
  suppressed: { email: string; reason: string }[];
}

export interface CheckRequest {
  kind: TemplateKind;
  subject: string;
  html: string;
  text?: string;
  variables?: Record<string, VariableValue>;
}

/** GET /templates/assets: `{ items, next_cursor }`, como los listados por cursor de contacts. */
interface RawAssetPage {
  items: TemplateAsset[] | null;
  next_cursor: string | null;
}

function toAssetPage(data: RawAssetPage | null): AssetPage {
  const next = data?.next_cursor?.trim();
  return { items: data?.items ?? [], nextCursor: next ? next : null };
}

/** Normaliza el kit: listas nulas de Go llegan como [] y el pie siempre tiene sus campos. */
function toBrandKit(data: Partial<BrandKit> | null): BrandKit {
  const footer = data?.footer;
  return {
    logo_asset_id: data?.logo_asset_id ?? null,
    colors: data?.colors ?? [],
    fonts: data?.fonts ?? [],
    footer: {
      company: footer?.company ?? '',
      address: footer?.address ?? '',
      website: footer?.website ?? '',
      support_email: footer?.support_email ?? '',
    },
    updated_at: data?.updated_at ?? null,
  };
}

function toCheckResult(data: CheckResult): CheckResult {
  return {
    ...data,
    issues: data.issues ?? [],
    spam: { ...data.spam, symbols: data.spam?.symbols ?? [] },
  };
}

function isIssue(value: unknown): value is CheckIssue {
  if (typeof value !== 'object' || value === null) return false;
  const issue = value as Record<string, unknown>;
  return (
    typeof issue.code === 'string' &&
    (issue.severity === 'error' || issue.severity === 'warning') &&
    typeof issue.message === 'string'
  );
}

/**
 * Issues del 409 DELIVERABILITY_FAILED al publicar, en `error.issues` del sobre de error.
 * null si el error es otro o no las trae; quien llama las pide entonces a
 * POST .../versions/{v}/check.
 */
export function deliverabilityIssues(err: unknown): CheckIssue[] | null {
  if (!isApiError(err) || !err.is(ERROR_CODES.DELIVERABILITY_FAILED)) return null;
  if (typeof err.body !== 'object' || err.body === null) return null;
  const error = (err.body as { error?: { issues?: unknown } }).error;
  return Array.isArray(error?.issues) ? error.issues.filter(isIssue) : null;
}

export const templatesApi = {
  list: (query: TemplateListQuery): Promise<Page<Template>> =>
    fetchPage<Template>(endpoints.templates.collection, { ...query }),
  get: (id: string) => api.get<TemplateDetail>(endpoints.templates.byId(id)),
  create: (input: CreateTemplateRequest) =>
    api.post<Template>(endpoints.templates.collection, { body: input }),
  update: (id: string, input: UpdateTemplateRequest) =>
    api.patch<Template>(endpoints.templates.byId(id), { body: input }),
  remove: (id: string) => api.delete<null>(endpoints.templates.byId(id)),

  listVersions: (id: string) => fetchList<VersionSummary>(endpoints.templates.versions(id)),
  createVersion: (id: string, content: TemplateContent) =>
    api.post<TemplateVersion>(endpoints.templates.versions(id), { body: content }),
  getVersion: (id: string, version: number) =>
    api.get<TemplateVersion>(endpoints.templates.version(id, version)),
  publish: (id: string, version: number) =>
    api.post<TemplateVersion>(endpoints.templates.publish(id, version)),

  /** Renderiza cualquier version, publicada o no, sin enviar nada. */
  preview: (id: string, input: RenderRequest) =>
    api.post<RenderedTemplate>(endpoints.templates.preview(id), { body: input }),

  /** Verifica un contenido sin guardarlo (panel en vivo del editor). */
  check: async (input: CheckRequest, signal?: AbortSignal): Promise<CheckResult> =>
    toCheckResult(
      (await api.post<CheckResult>(endpoints.templates.check, { body: input, signal })).data,
    ),
  checkVersion: async (id: string, version: number): Promise<CheckResult> =>
    toCheckResult(
      (await api.post<CheckResult>(endpoints.templates.versionCheck(id, version))).data,
    ),

  /** Envia la version, tambien un borrador, a unas pocas direcciones de prueba. */
  testSend: async (
    id: string,
    version: number,
    input: TestSendRequest,
  ): Promise<TestSendResult> => {
    const { data } = await api.post<TestSendResult>(endpoints.templates.testSend(id, version), {
      body: input,
    });
    return { messages: data.messages ?? [], suppressed: data.suppressed ?? [] };
  },

  brandKit: async (): Promise<BrandKit> =>
    toBrandKit((await api.get<Partial<BrandKit> | null>(endpoints.templates.brandKit)).data),
  saveBrandKit: async (input: BrandKitInput): Promise<BrandKit> =>
    toBrandKit(
      (await api.put<Partial<BrandKit> | null>(endpoints.templates.brandKit, { body: input })).data,
    ),

  listAssets: async (query: AssetListQuery = {}): Promise<AssetPage> =>
    toAssetPage(
      (
        await api.get<RawAssetPage | null>(endpoints.templates.assets, {
          params: { limit: query.limit, cursor: query.cursor },
        })
      ).data,
    ),
  uploadAsset: async (file: Blob, name: string): Promise<TemplateAsset> => {
    const form = new FormData();
    form.append('file', file, name);
    return (await api.post<TemplateAsset>(endpoints.templates.assets, { body: form })).data;
  },
  deleteAsset: (id: string) => api.delete<null>(endpoints.templates.asset(id)),

  meta: async (): Promise<TemplatesMeta> =>
    (await api.get<TemplatesMeta>(endpoints.templates.meta)).data,
};

/** Catalogo de plantillas compartido por todas las pantallas de la sesion. */
export const templatesMeta = cachedResource(templatesApi.meta);
