import { describe, expect, it } from 'vitest';
import {
  availablePresets,
  parseDomainLimit,
  presetRange,
  rangeDays,
  rangeProblem,
  shiftDay,
} from './analyticsRange';

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

  it('aplica el rango maximo que publica el servicio, con ambos extremos incluidos', () => {
    expect(rangeDays('2026-01-01', '2026-01-01')).toBe(1);
    expect(rangeDays('2025-12-31', '2026-01-01')).toBe(2);
    expect(rangeProblem('2026-01-01', '2026-01-31', 31)).toBeNull();
    expect(rangeProblem('2026-01-01', '2026-02-01', 31)).toBe('tooLong');
    expect(rangeProblem('2026-01-01', '', 1)).toBeNull();
  });

  it('solo ofrece los atajos que caben en el maximo, y siempre el rango por defecto', () => {
    expect(availablePresets(366, 30)).toEqual([7, 30, 90]);
    expect(availablePresets(60, 45)).toEqual([7, 30, 45]);
    expect(availablePresets(10, 10)).toEqual([7, 10]);
  });

  it('el tamano del ranking es un entero entre 1 y el maximo del servicio, o vacio', () => {
    expect(parseDomainLimit('', 100)).toEqual({ limit: undefined });
    expect(parseDomainLimit(' 25 ', 100)).toEqual({ limit: 25 });
    expect(parseDomainLimit('100', 100)).toEqual({ limit: 100 });
    expect(parseDomainLimit('101', 100)).toBeNull();
    expect(parseDomainLimit('0', 100)).toBeNull();
    expect(parseDomainLimit('2.5', 100)).toBeNull();
    expect(parseDomainLimit('1e3', 100)).toBeNull();
  });
});
