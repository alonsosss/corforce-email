import { describe, expect, it } from 'vitest';
import { normalizeImageHost } from './imageHosts';

describe('normalizeImageHost', () => {
  it('admite nombres de host y los normaliza', () => {
    expect(normalizeImageHost(' CDN.Tienda.Example. ')).toBe('cdn.tienda.example');
    expect(normalizeImageHost('xn--tnda-1qa.pe')).toBe('xn--tnda-1qa.pe');
  });

  it('rechaza esquemas, rutas, puertos, comodines y nombres sin dominio', () => {
    for (const bad of [
      'https://tienda.example',
      'tienda.example/img',
      'tienda.example:443',
      '*.tienda.example',
      'localhost',
      '-tienda.example',
      '',
    ]) {
      expect(normalizeImageHost(bad), bad).toBeNull();
    }
  });
});
