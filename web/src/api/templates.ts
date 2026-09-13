import { api } from './client';
import { endpoints } from './endpoints';
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

export interface TemplateVersion {
  id: string;
  template_id: string;
  version: number;
  subject: string;
  html: string;
  text: string | null;
  variables: TemplateVariable[] | null;
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
}

/** GET /templates/meta: valores de domain/entities.go y domain/variables.go. */
export interface TemplatesMeta {
  kinds: TemplateKind[];
  statuses: TemplateStatus[];
  version_statuses: VersionStatus[];
  variable_types: VariableType[];
  reserved_variables: ReservedVariable[];
  limits: TemplateLimits;
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

  meta: async (): Promise<TemplatesMeta> =>
    (await api.get<TemplatesMeta>(endpoints.templates.meta)).data,
};

/** Catalogo de plantillas compartido por todas las pantallas de la sesion. */
export const templatesMeta = cachedResource(templatesApi.meta);
