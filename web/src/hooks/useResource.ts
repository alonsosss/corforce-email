import type { CachedResource } from '@/api/resource';
import { useQuery, type QueryState } from './useQuery';

/** Lee un catalogo compartido con los estados de carga y error de useQuery. */
export function useResource<T>(resource: CachedResource<T>): QueryState<T> {
  return useQuery(() => resource.get(), [resource]);
}
