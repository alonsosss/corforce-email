import { describe, expect, it } from 'vitest';
import type { TemplateVariable, VariableType } from '@/api/templates';
import { t } from '@/i18n';
import {
  buildRenderVariables,
  coerceValue,
  draftsToVariables,
  initialValues,
  newVariableDraft,
  variablesToDrafts,
  type VariableDraft,
} from './variables';

function draft(
  name: string,
  type: VariableType,
  defaultValue = '',
  required = false,
): VariableDraft {
  return { ...newVariableDraft(type), name, defaultValue, required };
}

describe('declaracion de variables de una plantilla', () => {
  it('envia el valor por defecto con el tipo JSON declarado', () => {
    const { variables, errors } = draftsToVariables([
      draft('total', 'number', '12.5'),
      draft('vip', 'boolean', 'true'),
      draft('nombre', 'string', 'Ana'),
      draft('web', 'url', 'https://empresa.pe/oferta'),
      draft('correo', 'email', 'ana@empresa.pe'),
      draft('opcional', 'string'),
      draft('dato', 'string', '', true),
    ]);

    expect(errors).toEqual({});
    expect(variables).toEqual([
      { name: 'total', type: 'number', required: false, default: 12.5 },
      { name: 'vip', type: 'boolean', required: false, default: true },
      { name: 'nombre', type: 'string', required: false, default: 'Ana' },
      { name: 'web', type: 'url', required: false, default: 'https://empresa.pe/oferta' },
      { name: 'correo', type: 'email', required: false, default: 'ana@empresa.pe' },
      { name: 'opcional', type: 'string', required: false },
      { name: 'dato', type: 'string', required: true },
    ]);
  });

  it('rechaza nombres invalidos, duplicados, requeridas con defecto y defectos del tipo equivocado', () => {
    const drafts = [
      draft('Mayus', 'string'),
      draft('dup', 'string'),
      draft('dup', 'number'),
      draft('req', 'string', 'x', true),
      draft('n', 'number', 'doce'),
      draft('u', 'url', 'ftp://empresa.pe'),
      draft('e', 'email', 'no-es-correo'),
      draft('b', 'boolean', 'quizas'),
    ];
    const { variables, errors } = draftsToVariables(drafts);
    const errorOf = (index: number) => errors[drafts[index]?.key ?? ''];

    expect(variables.map((v) => v.name)).toEqual(['dup']);
    expect(Object.keys(errors)).toHaveLength(7);
    expect(errorOf(0)).toBe(t('templates.variables.invalidName'));
    expect(errorOf(2)).toBe(t('templates.variables.duplicate', { name: 'dup' }));
    expect(errorOf(3)).toBe(t('templates.variables.requiredNoDefault'));
    expect(errorOf(4)).toBe(t('templates.variables.expectNumber'));
    expect(errorOf(5)).toBe(t('templates.variables.expectUrl'));
    expect(errorOf(6)).toBe(t('templates.variables.expectEmail'));
    expect(errorOf(7)).toBe(t('templates.variables.expectBoolean'));
  });

  it('una declaracion guardada vuelve a enviarse igual tras editarla', () => {
    const declared: TemplateVariable[] = [
      { name: 'total', type: 'number', required: false, default: 3 },
      { name: 'activo', type: 'boolean', required: false, default: false },
      { name: 'nombre', type: 'string', required: true },
    ];
    expect(draftsToVariables(variablesToDrafts(declared)).variables).toEqual(declared);
  });

  it('las URL solo admiten http y https absolutas', () => {
    expect(coerceValue('url', 'https://empresa.pe').ok).toBe(true);
    expect(coerceValue('url', 'javascript:alert(1)').ok).toBe(false);
    expect(coerceValue('url', '/relativa').ok).toBe(false);
  });
});

describe('valores de previsualizacion', () => {
  const declared: TemplateVariable[] = [
    { name: 'nombre', type: 'string', required: true },
    { name: 'total', type: 'number', required: false },
    { name: 'vip', type: 'boolean', required: false, default: true },
    { name: 'web', type: 'url', required: false, default: 'https://empresa.pe' },
    { name: 'correo', type: 'email', required: false },
  ];

  it('parte de los valores por defecto declarados', () => {
    expect(initialValues(declared)).toEqual({
      nombre: '',
      total: '',
      vip: true,
      web: 'https://empresa.pe',
      correo: '',
    });
  });

  it('envia cada valor con su tipo y omite los opcionales vacios', () => {
    const result = buildRenderVariables(declared, {
      nombre: 'Ana',
      total: '42',
      vip: false,
      web: '',
      correo: '',
    });
    expect(result.errors).toEqual({});
    expect(result.variables).toEqual({ nombre: 'Ana', total: 42, vip: false });
  });

  it('marca la requerida vacia y los valores que no encajan con su tipo', () => {
    const result = buildRenderVariables(declared, {
      nombre: '  ',
      total: '1,5',
      vip: true,
      web: 'javascript:alert(1)',
      correo: 'sin-arroba',
    });
    expect(Object.keys(result.errors).sort()).toEqual(['correo', 'nombre', 'total', 'web']);
    expect(result.errors.nombre).toBe(t('validation.required'));
  });
});
