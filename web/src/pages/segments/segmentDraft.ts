import type { AttributeType } from '@/api/contacts';
import {
  isSegmentGroup,
  type FieldValueType,
  type OperatorArity,
  type SegmentCatalog,
  type SegmentCondition,
  type SegmentGroup,
  type SegmentMatch,
  type SegmentRule,
  type SegmentScalar,
} from '@/api/segments';
import { t } from '@/i18n';

// Modelo editable del DSL de segmentos. Campos, operadores, valores de los enumerados y
// limites salen del catalogo que publica GET /segments/meta; aqui no se copia ninguno. El
// servicio vuelve a compilar la definicion y responde con la regla que falla.

export type ValueKind = FieldValueType | AttributeType;

export interface DraftCondition {
  kind: 'condition';
  key: string;
  field: string;
  op: string;
  /** Valor de un operador de un solo valor, como texto. */
  value: string;
  /** Valores de un operador de lista (in). */
  values: string[];
}

export interface DraftGroup {
  kind: 'group';
  key: string;
  match: SegmentMatch;
  rules: DraftNode[];
}

export type DraftNode = DraftCondition | DraftGroup;

let sequence = 0;
function nextKey(): string {
  sequence += 1;
  return `rule-${sequence}`;
}

export interface FieldSpec {
  field: string;
  /** Clave del atributo declarado; null en los campos fijos. */
  attributeKey: string | null;
  valueKind: ValueKind;
  operators: string[];
  values: string[];
  /** Tope de N de los campos count. */
  max: number | null;
}

/** Campos de comportamiento: leen las aperturas y los clics registrados de cada contacto. */
export function isEngagementKind(kind: ValueKind): boolean {
  return kind === 'campaign' || kind === 'count';
}

/** Si alguna condicion del borrador usa aperturas o clics (para advertir de Apple Mail). */
export function usesEngagement(catalog: SegmentCatalog, group: DraftGroup): boolean {
  return group.rules.some((node) =>
    node.kind === 'group'
      ? usesEngagement(catalog, node)
      : isEngagementKind(specFor(catalog, node.field)?.valueKind ?? 'text'),
  );
}

export function fieldSpecs(catalog: SegmentCatalog): FieldSpec[] {
  const fixed: FieldSpec[] = catalog.fields.map((f) => ({
    field: f.field,
    attributeKey: null,
    valueKind: f.value_type,
    operators: f.operators,
    values: f.values ?? [],
    max: f.max ?? null,
  }));
  const attributes: FieldSpec[] = catalog.attributes.map((a) => ({
    field: `${catalog.attribute_prefix}${a.key}`,
    attributeKey: a.key,
    valueKind: a.type,
    operators: catalog.attribute_types.find((x) => x.type === a.type)?.operators ?? [],
    values: [],
    max: null,
  }));
  return [...fixed, ...attributes];
}

export function specFor(catalog: SegmentCatalog, field: string): FieldSpec | null {
  return fieldSpecs(catalog).find((s) => s.field === field) ?? null;
}

export function arityOf(catalog: SegmentCatalog, op: string): OperatorArity {
  return catalog.operators.find((o) => o.op === op)?.arity ?? 'one';
}

export function newCondition(catalog: SegmentCatalog): DraftCondition {
  const first = fieldSpecs(catalog)[0];
  return {
    kind: 'condition',
    key: nextKey(),
    field: first?.field ?? '',
    op: first?.operators[0] ?? '',
    value: '',
    values: [],
  };
}

export function newGroup(catalog: SegmentCatalog): DraftGroup {
  return {
    kind: 'group',
    key: nextKey(),
    match: catalog.match[0] ?? 'all',
    rules: [newCondition(catalog)],
  };
}

/** Otro campo: el operador vuelve al primero que admite y el valor se vacia. */
export function withField(
  catalog: SegmentCatalog,
  cond: DraftCondition,
  field: string,
): DraftCondition {
  const spec = specFor(catalog, field);
  return { ...cond, field, op: spec?.operators[0] ?? '', value: '', values: [] };
}

/** Otro operador: el valor se conserva si la aridad lo permite. */
export function withOperator(
  catalog: SegmentCatalog,
  cond: DraftCondition,
  op: string,
): DraftCondition {
  const from = arityOf(catalog, cond.op);
  const to = arityOf(catalog, op);
  if (from === to) return { ...cond, op };
  if (to === 'many') {
    return {
      ...cond,
      op,
      value: '',
      values: cond.value.trim() ? [cond.value.trim()] : cond.values,
    };
  }
  if (to === 'one') return { ...cond, op, value: cond.values[0] ?? cond.value, values: [] };
  return { ...cond, op, value: '', values: [] };
}

export function countRules(group: DraftGroup): number {
  return group.rules.reduce(
    (total, node) => total + 1 + (node.kind === 'group' ? countRules(node) : 0),
    0,
  );
}

function fromRule(rule: SegmentRule): DraftNode {
  if (isSegmentGroup(rule)) return fromDefinition(rule);
  const value = rule.value;
  return {
    kind: 'condition',
    key: nextKey(),
    field: rule.field,
    op: rule.op,
    value: value === undefined || Array.isArray(value) ? '' : String(value),
    values: Array.isArray(value) ? value.map(String) : [],
  };
}

export function fromDefinition(def: SegmentGroup): DraftGroup {
  return { kind: 'group', key: nextKey(), match: def.match, rules: def.rules.map(fromRule) };
}

const NUMBER = /^-?\d+(?:\.\d+)?(?:[eE][-+]?\d+)?$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;
const COUNT = /^[1-9]\d*$/;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export type Converted = { ok: true; value: SegmentScalar } | { ok: false; error: string };

export function convertValue(catalog: SegmentCatalog, spec: FieldSpec, raw: string): Converted {
  const text = raw.trim();
  if (!text) return { ok: false, error: t('segments.error.valueRequired') };
  if ([...text].length > catalog.limits.max_string_value) {
    return {
      ok: false,
      error: t('segments.error.valueTooLong', { n: catalog.limits.max_string_value }),
    };
  }
  switch (spec.valueKind) {
    case 'number': {
      const n = Number(text);
      return NUMBER.test(text) && Number.isFinite(n)
        ? { ok: true, value: n }
        : { ok: false, error: t('segments.error.expectNumber') };
    }
    case 'boolean':
      if (text === 'true' || text === 'false') return { ok: true, value: text === 'true' };
      return { ok: false, error: t('segments.error.expectBoolean') };
    case 'date':
    case 'timestamp':
      return DATE.test(text)
        ? { ok: true, value: text }
        : { ok: false, error: t('segments.error.expectDate') };
    case 'count': {
      const max = spec.max ?? 1;
      const n = Number(text);
      return COUNT.test(text) && n <= max
        ? { ok: true, value: n }
        : { ok: false, error: t('segments.error.expectCount', { max }) };
    }
    case 'campaign':
      return UUID.test(text)
        ? { ok: true, value: text.toLowerCase() }
        : { ok: false, error: t('segments.error.expectCampaign') };
    case 'enum':
      return spec.values.includes(text)
        ? { ok: true, value: text }
        : { ok: false, error: t('segments.error.invalidOption') };
    default:
      return { ok: true, value: text };
  }
}

function buildCondition(
  catalog: SegmentCatalog,
  node: DraftCondition,
): { rule: SegmentCondition } | { error: string } {
  const spec = specFor(catalog, node.field);
  if (!spec) return { error: t('segments.error.unknownField') };
  if (!spec.operators.includes(node.op)) return { error: t('segments.error.invalidOperator') };
  const arity = arityOf(catalog, node.op);
  if (arity === 'none') return { rule: { field: node.field, op: node.op } };
  if (arity === 'one') {
    const converted = convertValue(catalog, spec, node.value);
    return converted.ok
      ? { rule: { field: node.field, op: node.op, value: converted.value } }
      : { error: converted.error };
  }
  const values = node.values.map((v) => v.trim()).filter(Boolean);
  if (!values.length) return { error: t('segments.error.valuesRequired') };
  if (values.length > catalog.limits.max_in_values) {
    return { error: t('segments.error.tooManyValues', { n: catalog.limits.max_in_values }) };
  }
  const out: SegmentScalar[] = [];
  for (const v of values) {
    const converted = convertValue(catalog, spec, v);
    if (!converted.ok) return { error: converted.error };
    out.push(converted.value);
  }
  return { rule: { field: node.field, op: node.op, value: out } };
}

export interface BuildResult {
  definition: SegmentGroup | null;
  /** Error por clave de regla o de grupo. */
  errors: Record<string, string>;
}

export function toDefinition(draft: DraftGroup, catalog: SegmentCatalog): BuildResult {
  const errors: Record<string, string> = {};
  const walk = (group: DraftGroup, depth: number): SegmentGroup => {
    if (depth > catalog.limits.max_depth) {
      errors[group.key] = t('segments.error.tooDeep', { n: catalog.limits.max_depth });
    } else if (group.rules.length === 0) {
      errors[group.key] = t('segments.error.emptyGroup');
    }
    const rules: SegmentRule[] = [];
    for (const node of group.rules) {
      if (node.kind === 'group') {
        rules.push(walk(node, depth + 1));
        continue;
      }
      const built = buildCondition(catalog, node);
      if ('error' in built) errors[node.key] = built.error;
      else rules.push(built.rule);
    }
    return { match: group.match, rules };
  };
  const definition = walk(draft, 1);
  if (countRules(draft) > catalog.limits.max_rules && !errors[draft.key]) {
    errors[draft.key] = t('segments.error.tooManyRules', { n: catalog.limits.max_rules });
  }
  return Object.keys(errors).length ? { definition: null, errors } : { definition, errors };
}
