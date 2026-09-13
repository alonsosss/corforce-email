import { api } from './client';
import { endpoints } from './endpoints';
import { fetchList, fetchPage } from './paging';
import type { Page, PageQuery } from './types';

// DTOs de services/contacts/internal/adapters/http/handler.go, domain/entities.go,
// app/contacts.go (Export), app/consent.go (ConfirmationRequest) y app/lists.go.

export type ContactStatus = 'active' | 'unsubscribed' | 'bounced' | 'complained';
export type ConsentStatus = 'granted' | 'revoked' | 'pending' | 'none';
export type AttributeType = 'string' | 'number' | 'boolean' | 'date';
/** Valor guardado de un atributo: texto (tambien las fechas AAAA-MM-DD), numero o booleano. */
export type AttributeValue = string | number | boolean;

// contacts no publica aun un catalogo propio (GET /contacts/meta). Hasta entonces estas
// listas son espejo del servicio: domain.Statuses(), domain.AttrTypes() y lo que el
// handler admite al registrar consentimiento por API (la empresa concede o revoca, y solo
// declara api o form; el doble opt-in, la importacion y las bajas los registra la
// plataforma). El editor de segmentos no usa estas listas: lee GET /segments/meta.
export type ConsentGrantStatus = 'granted' | 'revoked';
export type ConsentApiMethod = 'api' | 'form';
export const CONTACT_STATUSES: readonly ContactStatus[] = [
  'active',
  'unsubscribed',
  'bounced',
  'complained',
];
export const ATTRIBUTE_TYPES: readonly AttributeType[] = ['string', 'number', 'boolean', 'date'];
export const CONSENT_API_METHODS: readonly ConsentApiMethod[] = ['api', 'form'];

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

export interface ContactImport {
  id: string;
  tenant_id: string;
  status: ImportStatus;
  total: number;
  created: number;
  updated: number;
  skipped: number;
  errors: ImportRowError[] | null;
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
};
