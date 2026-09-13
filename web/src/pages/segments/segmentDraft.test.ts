import { describe, expect, it } from 'vitest';
import type { SegmentGroup } from '@/api/segments';
import { t } from '@/i18n';
import { catalogFixture as catalog } from './catalogFixture';
import {
  countRules,
  fieldSpecs,
  fromDefinition,
  newCondition,
  newGroup,
  toDefinition,
  withField,
  withOperator,
  type DraftCondition,
  type DraftGroup,
} from './segmentDraft';

function cond(field: string, op: string, value = '', values: string[] = []): DraftCondition {
  return { ...newCondition(catalog), field, op, value, values };
}

function group(rules: DraftGroup['rules'], match: DraftGroup['match'] = 'all'): DraftGroup {
  return { ...newGroup(catalog), match, rules };
}

describe('definicion de un segmento desde el editor', () => {
  it('ofrece los campos fijos y los atributos declarados con los operadores de su tipo', () => {
    const specs = fieldSpecs(catalog);
    expect(specs.map((s) => s.field)).toEqual([
      'email',
      'status',
      'created_at',
      'tags',
      'list',
      'attributes.puntos',
      'attributes.vip',
    ]);
    expect(specs.find((s) => s.field === 'attributes.vip')?.operators).toEqual(['eq', 'exists']);
  });

  it('una definicion guardada vuelve al API igual tras pasar por el editor', () => {
    const def: SegmentGroup = {
      match: 'any',
      rules: [
        { field: 'status', op: 'in', value: ['active', 'bounced'] },
        { field: 'attributes.puntos', op: 'gt', value: 10 },
        {
          match: 'all',
          rules: [
            { field: 'attributes.vip', op: 'eq', value: true },
            { field: 'tags', op: 'exists' },
          ],
        },
      ],
    };
    expect(toDefinition(fromDefinition(def), catalog)).toEqual({ definition: def, errors: {} });
  });

  it('convierte cada valor al tipo del campo', () => {
    const draft = group([
      cond('attributes.puntos', 'in', '', ['1', '2.5']),
      cond('attributes.vip', 'eq', 'false'),
      cond('created_at', 'gt', '2026-01-31'),
    ]);
    expect(toDefinition(draft, catalog).definition?.rules).toEqual([
      { field: 'attributes.puntos', op: 'in', value: [1, 2.5] },
      { field: 'attributes.vip', op: 'eq', value: false },
      { field: 'created_at', op: 'gt', value: '2026-01-31' },
    ]);
  });

  it('marca cada regla que el compilador rechazaria, con su motivo', () => {
    const rules = [
      cond('email', 'eq', '  '),
      cond('attributes.puntos', 'gt', 'doce'),
      cond('status', 'eq', 'borrado'),
      cond('email', 'gt', 'a'),
      cond('tags', 'in', '', ['a', 'b', 'c', 'd']),
    ];
    const { definition, errors } = toDefinition(group(rules), catalog);
    expect(definition).toBeNull();
    const errorOf = (i: number) => errors[rules[i]?.key ?? ''];
    expect(errorOf(0)).toBe(t('segments.error.valueRequired'));
    expect(errorOf(1)).toBe(t('segments.error.expectNumber'));
    expect(errorOf(2)).toBe(t('segments.error.invalidOption'));
    expect(errorOf(3)).toBe(t('segments.error.invalidOperator'));
    expect(errorOf(4)).toBe(t('segments.error.tooManyValues', { n: 3 }));
  });

  it('aplica los limites de profundidad, de reglas y de grupos vacios del catalogo', () => {
    const deep = group([group([group([cond('tags', 'exists')])])]);
    const nested = (deep.rules[0] as DraftGroup).rules[0] as DraftGroup;
    expect(toDefinition(deep, catalog).errors[nested.key]).toBe(
      t('segments.error.tooDeep', { n: 2 }),
    );

    const empty = group([]);
    expect(toDefinition(empty, catalog).errors[empty.key]).toBe(t('segments.error.emptyGroup'));

    const many = group(Array.from({ length: 6 }, () => cond('tags', 'exists')));
    expect(countRules(many)).toBe(6);
    expect(toDefinition(many, catalog).errors[many.key]).toBe(
      t('segments.error.tooManyRules', { n: 5 }),
    );
  });

  it('al cambiar de campo u operador el valor se adapta a la nueva forma', () => {
    const base = cond('email', 'eq', 'ana@acme.test');
    const toIn = withOperator(catalog, base, 'in');
    expect(toIn).toMatchObject({ op: 'in', value: '', values: ['ana@acme.test'] });
    expect(withOperator(catalog, toIn, 'eq')).toMatchObject({ value: 'ana@acme.test', values: [] });
    expect(withField(catalog, base, 'status')).toMatchObject({ op: 'eq', value: '', values: [] });
  });
});
