import { describe, expect, it } from 'vitest';
import {
  barRatio,
  formatDecimalText,
  formatPercentText,
  formatRate,
  fractionToPercentText,
} from './decimal';

describe('texto decimal del API', () => {
  it('desplaza la coma sin redondear ni pasar por float', () => {
    expect(fractionToPercentText('0.2512')).toBe('25.12');
    expect(fractionToPercentText('0.0000')).toBe('0.00');
    expect(fractionToPercentText('1.0000')).toBe('100.00');
    expect(fractionToPercentText('0.0007')).toBe('0.07');
    expect(fractionToPercentText('0.1')).toBe('10');
    expect(fractionToPercentText('0.123456789012345678')).toBe('12.3456789012345678');
  });

  it('rechaza lo que no es un decimal sin signo', () => {
    expect(fractionToPercentText('-0.5')).toBeNull();
    expect(fractionToPercentText('NaN')).toBeNull();
    expect(fractionToPercentText('1e-3')).toBeNull();
    expect(formatRate('abc')).toBe('abc');
  });

  it('usa el separador decimal del idioma', () => {
    expect(formatRate('0.2512')).toBe('25,12 %');
    expect(formatDecimalText('49.00')).toBe('49,00');
    expect(formatPercentText('85.50')).toBe('85,50 %');
    expect(formatDecimalText('texto')).toBe('texto');
  });

  it('la barra se acota entre 0 y 1 y un porcentaje ausente no dibuja nada', () => {
    expect(barRatio('50.00')).toBe(0.5);
    expect(barRatio('180.00')).toBe(1);
    expect(barRatio(null)).toBe(0);
    expect(barRatio('x')).toBe(0);
  });
});
