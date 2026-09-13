import { describe, expect, it } from 'vitest';
import { detectDelimiter, parseCsv } from './csv';

describe('lectura de CSV', () => {
  it('respeta comillas, comillas escapadas y separadores dentro de comillas', () => {
    const text =
      'email,first_name,notas\r\nana@acme.test,"Ana, la ""jefa""","linea 1\nlinea 2"\r\n';
    expect(parseCsv(text)).toEqual([
      ['email', 'first_name', 'notas'],
      ['ana@acme.test', 'Ana, la "jefa"', 'linea 1\nlinea 2'],
    ]);
  });

  it('detecta el punto y coma de las hojas de calculo en espanol', () => {
    const text = 'email;first_name\nana@acme.test;Ana';
    expect(detectDelimiter(text)).toBe(';');
    expect(parseCsv(text)).toEqual([
      ['email', 'first_name'],
      ['ana@acme.test', 'Ana'],
    ]);
  });

  it('omite lineas vacias, la marca BOM y conserva celdas vacias intermedias', () => {
    expect(parseCsv('\uFEFFemail,tags\n\nana@acme.test,\n,\n')).toEqual([
      ['email', 'tags'],
      ['ana@acme.test', ''],
    ]);
  });
});
