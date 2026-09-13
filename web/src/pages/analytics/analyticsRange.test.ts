import { describe, expect, it } from 'vitest';
import { presetRange, rangeProblem, shiftDay } from './analyticsRange';

describe('rangos de los informes', () => {
  it('los atajos cuentan los dias hacia atras desde el ultimo dia del servicio', () => {
    expect(presetRange(7, '2026-03-01')).toEqual({ from: '2026-02-23', to: '2026-03-01' });
    expect(presetRange(1, '2026-03-01')).toEqual({ from: '2026-03-01', to: '2026-03-01' });
  });

  it('suma dias cruzando meses y anos bisiestos', () => {
    expect(shiftDay('2028-02-28', 1)).toBe('2028-02-29');
    expect(shiftDay('2026-01-01', -1)).toBe('2025-12-31');
  });

  it('rechaza fechas mal formadas y rangos invertidos', () => {
    expect(rangeProblem('2026-01-10', '2026-01-01')).toBe('reversed');
    expect(rangeProblem('10/01/2026', '')).toBe('invalidDate');
    expect(rangeProblem('', '')).toBeNull();
    expect(rangeProblem('2026-01-01', '2026-01-01')).toBeNull();
  });
});
