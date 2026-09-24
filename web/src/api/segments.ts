import { api } from './client';
import type { AttributeType, Contact } from './contacts';
import { endpoints } from './endpoints';
import { fetchPage } from './paging';
import type { Page, PageQuery } from './types';

// DTOs de services/contacts: domain.Segment, app.Preview y el catalogo del DSL
// (internal/segment/catalog.go) que publica GET /segments/meta con el esquema de la empresa.

export type SegmentMatch = 'all' | 'any';
export type SegmentScalar = string | number | boolean;
export type SegmentValue = SegmentScalar | SegmentScalar[];

export interface SegmentCondition {
  field: string;
  op: string;
  value?: SegmentValue;
}

export interface SegmentGroup {
  match: SegmentMatch;
  rules: SegmentRule[];
}

export type SegmentRule = SegmentCondition | SegmentGroup;

export function isSegmentGroup(rule: SegmentRule): rule is SegmentGroup {
  return 'rules' in rule;
}

export interface Segment {
  id: string;
  tenant_id: string;
  name: string;
  description: string;
  definition: SegmentGroup;
  created_at: string;
  updated_at: string;
}

export interface SegmentPreview {
  count: number;
  sample: Contact[];
}

export type OperatorArity = 'none' | 'one' | 'many';
/**
 * Forma del valor de un campo fijo; los atributos usan su tipo declarado. campaign es el id
 * de una campana y count un entero entre 1 y FieldInfo.max (reglas de comportamiento).
 */
export type FieldValueType =
  'email' | 'text' | 'enum' | 'timestamp' | 'tag' | 'list' | 'campaign' | 'count';

export interface OperatorInfo {
  op: string;
  arity: OperatorArity;
}

export interface FieldInfo {
  field: string;
  value_type: FieldValueType;
  operators: string[];
  values?: string[];
  /** Tope de N en los campos de valor count. */
  max?: number;
}

export interface AttributeTypeInfo {
  type: AttributeType;
  operators: string[];
}

export interface CatalogAttribute {
  key: string;
  type: AttributeType;
}

export interface SegmentLimits {
  max_depth: number;
  max_rules: number;
  max_in_values: number;
  max_string_value: number;
  max_definition_bytes: number;
  max_last_campaigns: number;
  max_last_days: number;
}

export interface SegmentCatalog {
  match: SegmentMatch[];
  attribute_prefix: string;
  operators: OperatorInfo[];
  fields: FieldInfo[];
  attribute_types: AttributeTypeInfo[];
  attributes: CatalogAttribute[];
  limits: SegmentLimits;
}

export interface SegmentRequest {
  name?: string;
  description?: string;
  definition?: SegmentGroup;
}

export const segmentsApi = {
  list: (query: PageQuery): Promise<Page<Segment>> =>
    fetchPage<Segment>(endpoints.segments.collection, { ...query }),
  get: (id: string) => api.get<Segment>(endpoints.segments.byId(id)),
  create: (input: SegmentRequest) =>
    api.post<Segment>(endpoints.segments.collection, { body: input }),
  update: (id: string, input: SegmentRequest) =>
    api.patch<Segment>(endpoints.segments.byId(id), { body: input }),
  remove: (id: string) => api.delete<null>(endpoints.segments.byId(id)),
  meta: async (): Promise<SegmentCatalog> =>
    (await api.get<SegmentCatalog>(endpoints.segments.meta)).data,
  /** Evalua sin guardar: el gateway lo gatea como lectura aunque viaje por POST. */
  preview: async (definition: SegmentGroup): Promise<SegmentPreview> =>
    (await api.post<SegmentPreview>(endpoints.segments.preview, { body: { definition } })).data,
  contacts: (id: string, query: PageQuery): Promise<Page<Contact>> =>
    fetchPage<Contact>(endpoints.segments.contacts(id), { ...query }),
};
