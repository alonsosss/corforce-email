import type { SegmentCatalog } from '@/api/segments';

/**
 * Catalogo de pruebas con la forma de GET /segments/meta (internal/segment/catalog.go).
 * Limites bajos a proposito para ejercitar los topes.
 */
export const catalogFixture: SegmentCatalog = {
  match: ['all', 'any'],
  attribute_prefix: 'attributes.',
  operators: [
    { op: 'eq', arity: 'one' },
    { op: 'neq', arity: 'one' },
    { op: 'contains', arity: 'one' },
    { op: 'gt', arity: 'one' },
    { op: 'in', arity: 'many' },
    { op: 'exists', arity: 'none' },
    { op: 'has_tag', arity: 'one' },
    { op: 'in_list', arity: 'one' },
  ],
  fields: [
    { field: 'email', value_type: 'email', operators: ['eq', 'neq', 'contains', 'in'] },
    {
      field: 'status',
      value_type: 'enum',
      operators: ['eq', 'neq', 'in'],
      values: ['active', 'unsubscribed', 'bounced', 'complained'],
    },
    { field: 'created_at', value_type: 'timestamp', operators: ['gt'] },
    { field: 'tags', value_type: 'tag', operators: ['has_tag', 'in', 'exists'] },
    { field: 'list', value_type: 'list', operators: ['in_list'] },
  ],
  attribute_types: [
    { type: 'string', operators: ['eq', 'contains', 'exists'] },
    { type: 'number', operators: ['eq', 'gt', 'in', 'exists'] },
    { type: 'boolean', operators: ['eq', 'exists'] },
    { type: 'date', operators: ['eq', 'gt', 'exists'] },
  ],
  attributes: [
    { key: 'puntos', type: 'number' },
    { key: 'vip', type: 'boolean' },
  ],
  limits: {
    max_depth: 2,
    max_rules: 5,
    max_in_values: 3,
    max_string_value: 20,
    max_definition_bytes: 65536,
  },
};
