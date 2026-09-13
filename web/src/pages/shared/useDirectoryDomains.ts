import { DIRECTORY_MAX_PAGE_SIZE, mailDirectoryApi } from '@/api/mailDirectory';
import { useQuery } from '@/hooks/useQuery';

/**
 * Dominios del directorio de la celda para los selectores de los formularios. Se pide la
 * primera pagina con el tope del API; una empresa con mas dominios que ese tope necesitara
 * busqueda en servidor.
 */
export function useDirectoryDomains() {
  return useQuery(
    async () =>
      (await mailDirectoryApi.listDomains({ page: 1, per_page: DIRECTORY_MAX_PAGE_SIZE })).items,
    [],
  );
}
