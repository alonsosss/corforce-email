import { describe, expect, it } from 'vitest';
import { capitalizeFirst } from './format';

describe('capitalizeFirst', () => {
  it('sube solo la primera letra y deja el resto como viene', () => {
    expect(capitalizeFirst('septiembre de 2026', 'es')).toBe('Septiembre de 2026');
    expect(capitalizeFirst('miércoles, 16 de septiembre', 'es')).toBe(
      'Miércoles, 16 de septiembre',
    );
    expect(capitalizeFirst('ñu', 'es')).toBe('Ñu');
    expect(capitalizeFirst('', 'es')).toBe('');
  });
});
