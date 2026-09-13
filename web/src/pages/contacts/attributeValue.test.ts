import { describe, expect, it } from 'vitest';
import type { AttributeDefinition, AttributeType } from '@/api/contacts';
import { t } from '@/i18n';
import { buildAttributes, parseAttribute } from './attributeValue';

function def(key: string, type: AttributeType, required = false): AttributeDefinition {
  return {
    id: key,
    tenant_id: 't',
    key,
    type,
    label: '',
    required,
    created_at: '',
    updated_at: '',
  };
}

const defs = [
  def('plan', 'string', true),
  def('puntos', 'number'),
  def('vip', 'boolean'),
  def('alta', 'date'),
];

describe('atributos declarados', () => {
  it('convierte cada valor al tipo JSON de su definicion', () => {
    expect(parseAttribute('number', '12.5')).toEqual({ ok: true, value: 12.5 });
    expect(parseAttribute('boolean', 'false')).toEqual({ ok: true, value: false });
    expect(parseAttribute('date', '2026-02-01')).toEqual({ ok: true, value: '2026-02-01' });
    expect(parseAttribute('string', ' con espacios ')).toEqual({
      ok: true,
      value: ' con espacios ',
    });
    expect(parseAttribute('number', '')).toEqual({ ok: true, value: null });
  });

  it('rechaza lo que no encaja con el tipo', () => {
    expect(parseAttribute('number', '1,5')).toEqual({
      ok: false,
      error: t('contacts.attributes.expectNumber'),
    });
    expect(parseAttribute('boolean', 'si').ok).toBe(false);
    expect(parseAttribute('date', '01/02/2026').ok).toBe(false);
  });

  it('en el alta solo viajan los valores escritos y se exigen los obligatorios', () => {
    expect(buildAttributes(defs, { puntos: '3', vip: '' }, null)).toEqual({
      attributes: { puntos: 3 },
      errors: { plan: t('validation.required') },
    });
  });

  it('en la edicion viajan los cambios y un valor borrado viaja como null', () => {
    const current = { plan: 'oro', puntos: 3, vip: true };
    const result = buildAttributes(defs, { plan: 'oro', puntos: '4', vip: '', alta: '' }, current);
    expect(result).toEqual({ attributes: { puntos: 4, vip: null }, errors: {} });
  });

  it('no deja borrar un obligatorio que tenia valor', () => {
    const result = buildAttributes(defs, { plan: '' }, { plan: 'oro' });
    expect(result.errors.plan).toBe(t('validation.required'));
  });
});
