import { describe, expect, it } from 'vitest';
import { t } from '@/i18n';
import { capitalizeFirst, formatDate, formatDateTime } from './format';

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

describe('fechas ausentes', () => {
  it('el time.Time vacio de Go no se muestra como una fecha', () => {
    expect(formatDateTime('0001-01-01T00:00:00Z')).toBe(t('common.dash'));
    expect(formatDate('0001-01-01T00:00:00Z')).toBe(t('common.dash'));
    expect(formatDateTime('2026-09-26T15:00:00Z')).not.toBe(t('common.dash'));
  });
});
