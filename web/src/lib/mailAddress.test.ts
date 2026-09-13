import { describe, expect, it } from 'vitest';
import { mergeChips, splitLines, splitTokens } from './listInput';
import {
  gotoToList,
  listToGoto,
  normalizeAliasAddress,
  normalizeDomainName,
  normalizeEmail,
  normalizeLocalPart,
} from './mailAddress';

describe('destinos de un alias: normalizacion', () => {
  it('pasa a minusculas y quita espacios y el punto final del dominio', () => {
    expect(normalizeEmail('  Ana.Torres@Empresa.PE ')).toBe('ana.torres@empresa.pe');
    expect(normalizeEmail('ana@empresa.pe.')).toBe('ana@empresa.pe');
  });

  it('admite en un destino externo caracteres que un buzon propio no admite', () => {
    expect(normalizeEmail("o'brien+tag@externo.com")).toBe("o'brien+tag@externo.com");
    expect(normalizeLocalPart("o'brien")).toBeNull();
    expect(normalizeLocalPart('Ana.Torres+ventas')).toBe('ana.torres+ventas');
  });

  it('rechaza direcciones mal formadas', () => {
    const bad = [
      '',
      'sin-arroba',
      '@dominio.com',
      'ana@',
      'ana@dominio',
      'ana@dominio.c0m',
      'ana..b@dominio.com',
      '.ana@dominio.com',
      'ana.@dominio.com',
      'ana@-dominio.com',
      'ana@dominio..com',
      'ana b@dominio.com',
      `${'a'.repeat(65)}@dominio.com`,
    ];
    for (const value of bad) expect(normalizeEmail(value), value).toBeNull();
  });

  it('el comodin @dominio vale como direccion del alias, nunca como destino', () => {
    expect(normalizeAliasAddress('@Empresa.pe')).toBe('@empresa.pe');
    expect(normalizeAliasAddress('@sin-tld')).toBeNull();
    expect(normalizeEmail('@empresa.pe')).toBeNull();
  });

  it('valida nombres de dominio como el directorio', () => {
    expect(normalizeDomainName('Correo.Empresa.COM.')).toBe('correo.empresa.com');
    expect(normalizeDomainName('localhost')).toBeNull();
    expect(normalizeDomainName(`${'a'.repeat(64)}.com`)).toBeNull();
  });

  it('goto se lee y se escribe como lista separada por comas', () => {
    expect(gotoToList(' a@x.pe, ,b@y.pe ')).toEqual(['a@x.pe', 'b@y.pe']);
    expect(listToGoto(['a@x.pe', 'b@y.pe'])).toBe('a@x.pe,b@y.pe');
    expect(gotoToList(listToGoto([]))).toEqual([]);
  });
});

describe('fichas de destinos: separacion y validacion', () => {
  it('separa por comas, punto y coma, espacios y saltos, y quita duplicados ya normalizados', () => {
    const merged = mergeChips(['a@x.pe'], 'B@Y.pe; a@x.pe\nc@z.pe  ,A@X.PE', normalizeEmail);
    expect(merged.values).toEqual(['a@x.pe', 'b@y.pe', 'c@z.pe']);
    expect(merged.rejected).toEqual([]);
  });

  it('devuelve aparte lo invalido sin perder lo valido ni tocar los valores actuales', () => {
    const current = ['ya@x.pe'];
    const merged = mergeChips(current, 'ok@x.pe, mal, otro@', normalizeEmail);
    expect(merged.values).toEqual(['ya@x.pe', 'ok@x.pe']);
    expect(merged.rejected).toEqual(['mal', 'otro@']);
    expect(current).toEqual(['ya@x.pe']);
  });

  it('una entrada por linea para las cargas masivas', () => {
    expect(splitLines(' a@x.pe \r\n\nb@y.pe\n  ')).toEqual(['a@x.pe', 'b@y.pe']);
    expect(splitTokens(' , ; ')).toEqual([]);
  });
});
