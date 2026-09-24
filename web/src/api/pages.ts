import { api } from './client';
import { endpoints } from './endpoints';
import { fetchPage } from './paging';
import { cachedResource } from './resource';
import type { Page, PageQuery } from './types';

// Paginas de aterrizaje de services/templates (docs/Plan_Marketing_Avanzado.md, 2-F): un
// documento del editor en modo web, versionado como las plantillas y publicado bajo la URL
// publica de la empresa. Los topes llegan en GET /templates/pages/meta.

export type LandingPageStatus = 'active' | 'archived';
export type PageVersionStatus = 'draft' | 'published' | 'superseded';

export const EDITOR_KIND_GRAPESJS_WEB = 'grapesjs-web';
export type PageEditorKind = typeof EDITOR_KIND_GRAPESJS_WEB;

export interface LandingPage {
  id: string;
  name: string;
  slug: string;
  status: LandingPageStatus;
  noindex: boolean;
  /** 0 mientras no hay ninguna version publicada. */
  current_version: number;
  public_url: string;
  created_by: string;
  created_at: string;
  updated_at: string;
}

/** getProjectData() de GrapesJS, opaco para el servidor: solo sirve para volver a editar. */
export interface PageEditor {
  kind: PageEditorKind;
  project: Record<string, unknown>;
}

export interface PageVersion {
  id: string;
  page_id: string;
  version: number;
  title: string;
  description: string;
  /** Saneado por el servidor al guardar. */
  html: string;
  css: string;
  editor: PageEditor | null;
  status: PageVersionStatus;
  published_at: string | null;
  created_by: string;
  created_at: string;
}

export interface PageVersionSummary {
  id: string;
  version: number;
  status: PageVersionStatus;
  published_at: string | null;
  created_by: string;
  created_at: string;
}

export interface PageDetail {
  page: LandingPage;
  current: PageVersion | null;
  versions: PageVersionSummary[];
}

export interface PageContent {
  title: string;
  description: string;
  html: string;
  css: string;
  editor: PageEditor;
}

export interface CreatePageRequest {
  name: string;
  slug: string;
  noindex?: boolean;
  content?: PageContent;
}

export interface UpdatePageRequest {
  name?: string;
  slug?: string;
  noindex?: boolean;
  status?: LandingPageStatus;
}

export interface PageListQuery extends PageQuery {
  status?: LandingPageStatus;
  search?: string;
}

export interface PagesMeta {
  editor_kinds: PageEditorKind[];
  max_html_bytes: number;
  max_css_bytes: number;
  max_editor_bytes: number;
  max_name_length: number;
  max_title_length: number;
  max_description_length: number;
  max_slug_length: number;
  /** Expresion regular del slug, como la valida el servicio. */
  slug_pattern: string;
  /** URL publica bajo la que cuelga el slug. */
  public_prefix: string;
}

function toDetail(data: PageDetail): PageDetail {
  return { ...data, versions: data.versions ?? [] };
}

export const pagesApi = {
  list: (query: PageListQuery): Promise<Page<LandingPage>> =>
    fetchPage<LandingPage>(endpoints.templates.pages.collection, { ...query }),
  get: async (id: string): Promise<PageDetail> =>
    toDetail((await api.get<PageDetail>(endpoints.templates.pages.byId(id))).data),
  create: async (input: CreatePageRequest): Promise<PageDetail> =>
    toDetail(
      (await api.post<PageDetail>(endpoints.templates.pages.collection, { body: input })).data,
    ),
  update: async (id: string, input: UpdatePageRequest): Promise<PageDetail> =>
    toDetail(
      (await api.patch<PageDetail>(endpoints.templates.pages.byId(id), { body: input })).data,
    ),
  /** Solo una pagina archivada; si no, 409. */
  remove: (id: string) => api.delete<null>(endpoints.templates.pages.byId(id)),

  createVersion: async (id: string, content: PageContent): Promise<PageVersion> =>
    (await api.post<PageVersion>(endpoints.templates.pages.versions(id), { body: content })).data,
  getVersion: async (id: string, version: number): Promise<PageVersion> =>
    (await api.get<PageVersion>(endpoints.templates.pages.version(id, version))).data,
  publish: async (id: string, version: number): Promise<PageDetail> =>
    toDetail((await api.post<PageDetail>(endpoints.templates.pages.publish(id, version))).data),
  unpublish: async (id: string): Promise<PageDetail> =>
    toDetail((await api.post<PageDetail>(endpoints.templates.pages.unpublish(id))).data),

  meta: async (): Promise<PagesMeta> =>
    (await api.get<PagesMeta>(endpoints.templates.pages.meta)).data,
};

/** Catalogo de paginas compartido por todas las pantallas de la sesion. */
export const pagesMeta = cachedResource(pagesApi.meta);
