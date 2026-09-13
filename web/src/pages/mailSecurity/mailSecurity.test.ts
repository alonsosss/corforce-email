import { describe, expect, it } from 'vitest';
import {
  isDecimal,
  normalizeListPattern,
  normalizePolicyObject,
  policyObjectKind,
} from './policyObject';
import { formatRateLimit, parseRateLimit } from './rateLimit';

describe('limites de tasa "N / 1h"', () => {
  it('lee y compone el formato de ratelimit.lua', () => {
    expect(parseRateLimit('100 / 1h')).toEqual({ amount: '100', unit: 'h' });
    expect(parseRateLimit('5 / 1s')).toEqual({ amount: '5', unit: 's' });
    expect(formatRateLimit('100', 'h')).toBe('100 / 1h');
    expect(formatRateLimit('007', 'm')).toBe('7 / 1m');
  });

  it('rechaza lo que Rspamd no sabria leer', () => {
    for (const bad of ['100/1h', '100 / 2h', '100 / 1w', '-1 / 1h', '1.5 / 1h', '']) {
      expect(parseRateLimit(bad), bad).toBeNull();
    }
    expect(formatRateLimit('1.5', 'h')).toBeNull();
    expect(formatRateLimit('', 'h')).toBeNull();
    expect(formatRateLimit('9'.repeat(20), 'h')).toBeNull();
  });
});

describe('objetos y patrones de politica', () => {
  it('un objeto es un buzon o un dominio, en minusculas', () => {
    expect(normalizePolicyObject(' Ana@Empresa.PE ')).toBe('ana@empresa.pe');
    expect(normalizePolicyObject('Empresa.pe')).toBe('empresa.pe');
    expect(policyObjectKind('ana@empresa.pe')).toBe('mailbox');
    expect(policyObjectKind('empresa.pe')).toBe('domain');
    for (const bad of ['*@empresa.pe', '@empresa.pe', 'ana@', 'sin-punto', 'a b@empresa.pe']) {
      expect(normalizePolicyObject(bad), bad).toBeNull();
    }
  });

  it('un patron admite direccion, @dominio o comodin, pero no solo * ni dos @', () => {
    expect(normalizeListPattern('*@Spam.test')).toBe('*@spam.test');
    expect(normalizeListPattern('@spam.test')).toBe('@spam.test');
    for (const bad of ['*', '@', '', 'a@b@c.test', 'con espacio@x.test', 'regex(.*)']) {
      expect(normalizeListPattern(bad), bad).toBeNull();
    }
  });

  it('las puntuaciones son decimales con signo', () => {
    expect(isDecimal('5')).toBe(true);
    expect(isDecimal('-2.5')).toBe(true);
    expect(isDecimal('1e3')).toBe(false);
    expect(isDecimal('')).toBe(false);
  });
});
