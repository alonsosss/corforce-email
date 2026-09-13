import { api } from './client';
import { endpoints } from './endpoints';
import { fetchList, fetchPage } from './paging';
import { cachedResource } from './resource';
import type { Page, PageQuery } from './types';

// DTOs de services/contacts/internal/adapters/http/handler.go, domain/entities.go,
// app/contacts.go (Export), app/consent.go (ConfirmationRequest) y app/lists.go. Los
// valores admitidos y los topes llegan en GET /contacts/meta (adapters/http/meta.go); el
// editor de segmentos usa su propio catalogo, GET /segments/meta.

/** Estado de entrega del contacto: los valores admitidos llegan en GET /contacts/meta. */
export type ContactStatus = string;
/** Que levanta un estado de exclusion (status_details[].lifted_by); ausente en active. */
export type ContactStatusLift = string;
export type ConsentStatus = 'granted' | 'revoked' | 'pending' | 'none';
export type AttributeType = 'string' | 'number' | 'boolean' | 'date';
/** Valor guardado de un atributo: texto (tambien las fechas AAAA-MM-DD), numero o booleano. */
export type AttributeValue = string | number | boolean;

/** Lo que la empresa puede registrar por API: concede o revoca, y declara api o form. */
export type ConsentGrantStatus = 'granted' | 'revoked';
export type ConsentApiMethod = 'api' | 'form';
export type ImportConsentStatus = 'granted' | 'none';

/**
 * Un estado del contacto: con varias causas de exclusion vigentes el estado es el de mayor
 * gravedad, y lifted_by dice que lo levanta.
 */
export interface ContactStatusDetail {
  status: ContactStatus;
  severity: number;
  lifted_by?: ContactStatusLift;
}

/** GET /contacts/meta: valores del dominio y topes efectivos del servicio. */
export interface ContactsMeta {
  statuses: ContactStatus[];
  /** En el mismo orden que statuses. */
  status_details: ContactStatusDetail[];
  consent_statuses: ConsentStatus[];
  consent_methods: string[];
  api_consent_statuses: ConsentGrantStatus[];
  api_consent_methods: ConsentApiMethod[];
  sources: string[];
  attribute_types: AttributeType[];
  import: {
    max_rows: number;
    max_errors: number;
    consent_statuses: ImportConsentStatus[];
    max_consent_basis_length: number;
  };
  limits: {
    max_email_length: number;
    max_name_length: number;
    max_tags: number;
    max_tag_length: number;
    max_attribute_definitions: number;
    max_attribute_string_length: number;
    max_consent_source_length: number;
    max_search_length: number;
  };
  pagination: { default_page_size: number; max_page_size: number };
}

export interface Contact {
  id: string;
  tenant_id: string;
  email: string;
  first_name: string;
  last_name: string;
  locale: string | null;
  timezone: string | null;
  attributes: Record<string, AttributeValue> | null;
  tags: string[] | null;
  status: ContactStatus;
  consent_status: ConsentStatus;
  source: string;
  created_at: string;
  updated_at: string;
}

export interface Consent {
  id: string;
  tenant_id: string;
  contact_id: string;
  purpose: string;
  status: ConsentStatus;
  method: string;
  source: string;
  ip: string | null;
  user_agent: string | null;
  evidence: Record<string, unknown> | null;
  occurred_at: string;
}

export interface ContactList {
  id: string;
  tenant_id: string;
  name: string;
  description: string;
  member_count: number;
  created_at: string;
  updated_at: string;
}

export interface AttributeDefinition {
  id: string;
  tenant_id: string;
  key: string;
  type: AttributeType;
  label: string;
  required: boolean;
  created_at: string;
  updated_at: string;
}

export interface ImportRowError {
  line: number;
  reason: string;
}

export type ImportStatus = 'completed' | 'failed';

/**
 * De los contactos creados, cuantos entraron ya excluidos por una causa vigente en
 * suppression, por estado (sin active). Siempre un objeto; vacio en las importaciones
 * anteriores a la comprobacion.
 */
export type ImportSuppressedCounts = Record<ContactStatus, number>;

export interface ContactImport {
  id: string;
  tenant_id: string;
  status: ImportStatus;
  total: number;
  created: number;
  updated: number;
  skipped: number;
  errors: ImportRowError[] | null;
  suppressed?: ImportSuppressedCounts;
  consent_basis: string;
  list_id: string | null;
  created_by: string;
  created_at: string;
}

export interface ImportResult {
  id: string;
  status: ImportStatus;
  total: number;
  created: number;
  updated: number;
  skipped: number;
  errors: ImportRowError[] | null;
  suppressed?: ImportSuppressedCounts;
}

export interface ContactExport {
  contact: Contact;
  lists: ContactList[];
  consents: Consent[];
  exported_at: string;
}

export interface ConfirmationRequest {
  contact_id: string;
  status: ConsentStatus;
  expires_at: string;
}

export interface MembersResult {
  added: number;
  ignored: number;
}

export interface RemovedMembersResult {
  removed: number;
  ignored: number;
}

export interface ContactQuery extends PageQuery {
  search?: string;
  status?: ContactStatus;
  tag?: string;
  list_id?: string;
}

export interface ConsentDeclaration {
  status: ConsentGrantStatus;
  method: ConsentApiMethod;
  source: string;
  ip?: string;
  user_agent?: string;
}

export interface CreateContactRequest {
  email: string;
  first_name?: string;
  last_name?: string;
  locale?: string;
  timezone?: string;
  attributes?: Record<string, AttributeValue>;
  tags?: string[];
  consent?: ConsentDeclaration;
}

/**
 * PATCH: un campo ausente no cambia. locale y timezone vacios se borran; attributes se
 * mezcla (null quita la clave) y tags reemplaza la lista.
 */
export interface UpdateContactRequest {
  first_name?: string;
  last_name?: string;
  locale?: string;
  timezone?: string;
  attributes?: Record<string, AttributeValue | null>;
  tags?: string[];
}

export interface ImportRow {
  email: string;
  first_name?: string;
  last_name?: string;
  locale?: string;
  timezone?: string;
  attributes?: Record<string, AttributeValue>;
  tags?: string[];
}

export interface ImportRequest {
  rows: ImportRow[];
  list_id?: string;
  update_existing: boolean;
  /** Sin consentimiento declarado el servicio registra none. */
  consent?: { status: 'granted'; basis: string } | { status: 'none' };
}

export interface ListRequest {
  name?: string;
  description?: string;
}

export interface CreateAttributeRequest {
  key: string;
  type: AttributeType;
  label: string;
  required: boolean;
}

export interface UpdateAttributeRequest {
  label?: string;
  required?: boolean;
}

export const contactsApi = {
  list: (query: ContactQuery): Promise<Page<Contact>> =>
    fetchPage<Contact>(endpoints.contacts.collection, { ...query }),
  get: (id: string) => api.get<Contact>(endpoints.contacts.byId(id)),
  create: (input: CreateContactRequest) =>
    api.post<Contact>(endpoints.contacts.collection, { body: input }),
  update: (id: string, input: UpdateContactRequest) =>
    api.patch<Contact>(endpoints.contacts.byId(id), { body: input }),
  /** Derecho de supresion: borra al contacto y seudonimiza su evidencia. */
  remove: (id: string) => api.delete<null>(endpoints.contacts.byId(id)),
  /** Derecho de acceso: todo lo que la empresa guarda de la persona. */
  exportData: (id: string) => api.get<ContactExport>(endpoints.contacts.export(id)),

  listConsents: (id: string) => fetchList<Consent>(endpoints.contacts.consents(id)),
  recordConsent: (id: string, input: ConsentDeclaration) =>
    api.post<Consent>(endpoints.contacts.consent(id), { body: input }),
  /** Doble opt-in: el enlace no vuelve a la empresa, lo envia la plataforma. */
  requestConfirmation: (id: string, source?: string) =>
    api.post<ConfirmationRequest>(endpoints.contacts.consentRequest(id), {
      body: source ? { source } : {},
    }),

  importContacts: (input: ImportRequest) =>
    api.post<ImportResult>(endpoints.contacts.imports.collection, { body: input }),
  listImports: (query: PageQuery): Promise<Page<ContactImport>> =>
    fetchPage<ContactImport>(endpoints.contacts.imports.collection, { ...query }),

  listLists: (query: PageQuery): Promise<Page<ContactList>> =>
    fetchPage<ContactList>(endpoints.contacts.lists.collection, { ...query }),
  getList: (id: string) => api.get<ContactList>(endpoints.contacts.lists.byId(id)),
  createList: (input: ListRequest) =>
    api.post<ContactList>(endpoints.contacts.lists.collection, { body: input }),
  updateList: (id: string, input: ListRequest) =>
    api.patch<ContactList>(endpoints.contacts.lists.byId(id), { body: input }),
  deleteList: (id: string) => api.delete<null>(endpoints.contacts.lists.byId(id)),
  addMembers: (id: string, contactIds: string[]) =>
    api.post<MembersResult>(endpoints.contacts.listMembers(id), {
      body: { contact_ids: contactIds },
    }),
  /** Baja en bloque por POST: quitar miembros es editar la lista, no borrar. */
  removeMembers: (id: string, contactIds: string[]) =>
    api.post<RemovedMembersResult>(endpoints.contacts.listMembersRemove(id), {
      body: { contact_ids: contactIds },
    }),

  listAttributes: () => fetchList<AttributeDefinition>(endpoints.contacts.attributes),
  createAttribute: (input: CreateAttributeRequest) =>
    api.post<AttributeDefinition>(endpoints.contacts.attributes, { body: input }),
  updateAttribute: (key: string, input: UpdateAttributeRequest) =>
    api.patch<AttributeDefinition>(endpoints.contacts.attribute(key), { body: input }),
  deleteAttribute: (key: string) => api.delete<null>(endpoints.contacts.attribute(key)),

  meta: async (): Promise<ContactsMeta> =>
    (await api.get<ContactsMeta>(endpoints.contacts.meta)).data,
};

/** Catalogo de contactos compartido por todas las pantallas de la sesion. */
export const contactsMeta = cachedResource(contactsApi.meta);
