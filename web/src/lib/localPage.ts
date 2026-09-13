import type { Page } from '@/api/types';

/**
 * Pagina de una lista que el API entrega completa. La pagina pedida se acota a la ultima
 * que existe: tras un filtro o una recarga que deja menos filas no queda una tabla vacia.
 */
export function localPage<T>(items: readonly T[], page: number, perPage: number): Page<T> {
  const size = Math.max(1, Math.floor(perPage));
  const total = items.length;
  const totalPages = Math.ceil(total / size);
  const current = Math.min(Math.max(1, Math.floor(page)), Math.max(totalPages, 1));
  const start = (current - 1) * size;
  return {
    items: items.slice(start, start + size),
    page: current,
    perPage: size,
    total,
    totalPages,
  };
}
