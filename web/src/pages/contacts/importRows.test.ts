import { describe, expect, it } from 'vitest';
import type { AttributeDefinition, AttributeType } from '@/api/contacts';
import { rowsFromCsv } from './importRows';

function def(key: string, type: AttributeType): AttributeDefinition {
  return {
    id: key,
    tenant_id: 't',
    key,
    type,
    label: '',
    required: false,
    created_at: '',
    updated_at: '',
  };
}

const defs = [def('puntos', 'number'), def('vip', 'boolean')];

describe('filas de importacion desde CSV', () => {
  it('reparte campos fijos, etiquetas y atributos con su tipo', () => {
    const csv = [
      'Email;first_name;tags;attributes.puntos;vip',
      'ana@acme.test;Ana;cliente|zona norte;12;true',
      'luis@acme.test;;;;',
    ].join('\n');
    const parsed = rowsFromCsv(csv, defs);
    expect(parsed.missingEmail).toBe(false);
    expect(parsed.unknownColumns).toEqual([]);
    expect(parsed.rows).toEqual([
      {
        email: 'ana@acme.test',
        first_name: 'Ana',
        tags: ['cliente', 'zona norte'],
        attributes: { puntos: 12, vip: true },
      },
      { email: 'luis@acme.test' },
    ]);
  });

  it('senala las columnas que no son campos ni atributos declarados', () => {
    const parsed = rowsFromCsv('email,telefono,puntos\nana@acme.test,555,1', defs);
    expect(parsed.unknownColumns).toEqual(['telefono']);
  });

  it('avisa si falta la columna email', () => {
    expect(rowsFromCsv('first_name\nAna', defs).missingEmail).toBe(true);
  });

  it('un valor que no encaja con su tipo viaja tal cual para que el servicio lo rechace', () => {
    const parsed = rowsFromCsv('email,puntos\nana@acme.test,doce', defs);
    expect(parsed.rows[0]?.attributes).toEqual({ puntos: 'doce' });
  });
});
