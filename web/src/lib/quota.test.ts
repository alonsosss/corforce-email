import { describe, expect, it } from 'vitest';
import { t } from '@/i18n';
import { bytesToQuota, formatQuota, quotaToBytes, usageRatio } from './quota';

const MIB = 1024 ** 2;
const GIB = 1024 ** 3;

describe('cuotas: MiB y GiB a bytes', () => {
  it('convierte cantidades enteras y decimales a bytes enteros', () => {
    expect(quotaToBytes('512', 'MiB')).toBe(512 * MIB);
    expect(quotaToBytes('1', 'GiB')).toBe(GIB);
    expect(quotaToBytes('1.5', 'GiB')).toBe(1.5 * GIB);
    expect(quotaToBytes('1,5', 'GiB')).toBe(1.5 * GIB);
    expect(quotaToBytes(' 0 ', 'MiB')).toBe(0);
  });

  it('redondea a byte entero: el API guarda int64', () => {
    expect(Number.isInteger(quotaToBytes('0.3', 'MiB'))).toBe(true);
    expect(quotaToBytes('0.0000001', 'MiB')).toBe(0);
  });

  it('rechaza lo que no es una cantidad no negativa y finita', () => {
    for (const bad of ['', ' ', '-1', 'abc', '1e3', 'Infinity', 'NaN', '1.', '.5', '1 000']) {
      expect(quotaToBytes(bad, 'GiB'), bad).toBeNull();
    }
    expect(quotaToBytes('9'.repeat(20), 'GiB')).toBeNull();
  });
});

describe('cuotas: bytes a MiB y GiB', () => {
  it('usa GiB solo cuando la cantidad es exacta', () => {
    expect(bytesToQuota(GIB)).toEqual({ amount: '1', unit: 'GiB' });
    expect(bytesToQuota(10 * GIB)).toEqual({ amount: '10', unit: 'GiB' });
    expect(bytesToQuota(1.5 * GIB)).toEqual({ amount: '1536', unit: 'MiB' });
    expect(bytesToQuota(512 * MIB)).toEqual({ amount: '512', unit: 'MiB' });
  });

  it('0, negativos y valores no finitos se editan como 0', () => {
    for (const value of [0, -1, Number.NaN, Number.POSITIVE_INFINITY]) {
      expect(bytesToQuota(value)).toEqual({ amount: '0', unit: 'MiB' });
    }
  });

  it('ida y vuelta sin perder bytes en multiplos de MiB', () => {
    for (const bytes of [0, MIB, 700 * MIB, GIB, 3 * GIB + 256 * MIB]) {
      const { amount, unit } = bytesToQuota(bytes);
      expect(quotaToBytes(amount, unit)).toBe(bytes);
    }
  });
});

describe('cuotas: presentacion', () => {
  it('0 es sin limite y el resto va en unidades binarias', () => {
    expect(formatQuota(0)).toBe(t('quota.unlimited'));
    expect(formatQuota(GIB)).toBe('1 GiB');
    expect(formatQuota(512 * MIB)).toBe('512 MiB');
  });

  it('la proporcion de uso queda entre 0 y 1 y es null sin limite o con datos invalidos', () => {
    expect(usageRatio(50, 100)).toBe(0.5);
    expect(usageRatio(150, 100)).toBe(1);
    expect(usageRatio(10, 0)).toBeNull();
    expect(usageRatio(Number.NaN, 100)).toBeNull();
    expect(usageRatio(-1, 100)).toBeNull();
  });
});
