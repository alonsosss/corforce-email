import { describe, expect, it } from 'vitest';
import { localPage } from './localPage';

const items = Array.from({ length: 45 }, (_, i) => i + 1);

describe('pagina de una lista completa', () => {
  it('corta la pagina pedida y calcula el total de paginas', () => {
    expect(localPage(items, 3, 20)).toEqual({
      items: [41, 42, 43, 44, 45],
      page: 3,
      perPage: 20,
      total: 45,
      totalPages: 3,
    });
  });

  it('una pagina que ya no existe se acota a la ultima', () => {
    expect(localPage([1, 2], 5, 20)).toMatchObject({ items: [1, 2], page: 1, totalPages: 1 });
    expect(localPage([], 2, 20)).toMatchObject({ items: [], page: 1, total: 0, totalPages: 0 });
  });
});
