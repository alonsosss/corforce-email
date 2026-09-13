import { api, type QueryParams } from './client';
import { toPage, type Page, type PageQuery } from './types';

// Reserva cuando el meta del envelope no trae la pagina (omitempty en Go). Las pantallas
// siempre piden page y per_page, asi que solo se usa ante una respuesta incompleta.
const FALLBACK_PAGE = 1;
const FALLBACK_PER_PAGE = 20;

/**
 * per_page de los selectores (listas, segmentos, plantillas, empresas): el maximo que
 * admiten los servicios (maxPerPage en sus handlers). Mas alla, el selector lo avisa.
 */
export const PICKER_PAGE_SIZE = 100;

/** GET de un listado paginado: manda el meta del envelope, no lo que se pidio. */
export async function fetchPage<T>(path: string, query: PageQuery & QueryParams): Promise<Page<T>> {
  const res = await api.get<T[]>(path, { params: query });
  return toPage(res, {
    page: query.page ?? FALLBACK_PAGE,
    per_page: query.per_page ?? FALLBACK_PER_PAGE,
  });
}

/** GET de un listado sin paginar. Un slice vacio de Go llega como null: aqui es []. */
export async function fetchList<T>(path: string, params?: QueryParams): Promise<T[]> {
  const res = await api.get<T[] | null>(path, { params });
  return res.data ?? [];
}
