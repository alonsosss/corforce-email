import { describe, expect, it } from 'vitest';
import { applyFilter, cssFilter, IMAGE_FILTERS } from './imageFilters';

function pixel(r: number, g: number, b: number, a = 255): Uint8ClampedArray {
  return new Uint8ClampedArray([r, g, b, a]);
}

describe('filtros del editor de imagenes', () => {
  it('blanco y negro deja los tres canales iguales y respeta el alfa', () => {
    const data = pixel(200, 100, 50, 128);
    applyFilter(data, 'grayscale');
    expect(data[0]).toBe(data[1]);
    expect(data[1]).toBe(data[2]);
    expect(data[3]).toBe(128);
  });

  it('brillo y contraste recortan a 0..255', () => {
    const bright = pixel(250, 10, 128);
    applyFilter(bright, 'brighten');
    expect(bright[0]).toBe(255);
    const contrast = pixel(0, 255, 128);
    applyFilter(contrast, 'contrast');
    expect(contrast[0]).toBe(0);
    expect(contrast[1]).toBe(255);
    expect(contrast[2]).toBe(128);
  });

  it('ninguno no cambia nada y cada filtro tiene su equivalente CSS', () => {
    const data = pixel(1, 2, 3);
    applyFilter(data, 'none');
    expect([...data]).toEqual([1, 2, 3, 255]);
    for (const filter of IMAGE_FILTERS) expect(cssFilter(filter)).toBeTruthy();
  });
});
