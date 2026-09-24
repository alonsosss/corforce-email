import { describe, expect, it } from 'vitest';
import { t } from '@/i18n';
import { publicUrlFor, slugError, slugFromName } from './slug';

const META = { max_slug_length: 20, slug_pattern: '^[a-z0-9]+(?:-[a-z0-9]+)*$' };

describe('direccion de una pagina', () => {
  it.each([
    ['Oferta de Otoño 2026', 'oferta-de-otono-2026'],
    ['  ¡Únete ya!  ', 'unete-ya'],
    ['Precios -- y planes', 'precios-y-planes'],
    ['***', ''],
  ])('%s da %s', (name, slug) => {
    expect(slugFromName(name, 60)).toBe(slug);
  });

  it('se recorta al tope sin dejar un guion al final', () => {
    expect(slugFromName('Campana de lanzamiento del producto', 20)).toBe('campana-de-lanzamien');
    expect(slugFromName('Campana de la tienda', 12)).toBe('campana-de-l');
    expect(slugFromName('Campana de', 8)).toBe('campana');
  });

  it('valida con el patron y el tope del servicio', () => {
    expect(slugError('', META)).toBe(t('validation.required'));
    expect(slugError('oferta-2026', META)).toBeNull();
    expect(slugError('Oferta', META)).toBe(t('validation.slug'));
    expect(slugError('-oferta', META)).toBe(t('validation.slug'));
    expect(slugError('a'.repeat(21), META)).toBe(t('validation.maxLength', { n: 20 }));
  });

  it('la URL publica cuelga del prefijo con o sin barra final', () => {
    expect(publicUrlFor('https://p.empresa.test', 'oferta')).toBe('https://p.empresa.test/oferta');
    expect(publicUrlFor('https://p.empresa.test/', 'oferta')).toBe('https://p.empresa.test/oferta');
  });
});
