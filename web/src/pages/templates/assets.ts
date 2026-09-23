import { templatesApi, type TemplateAsset } from '@/api/templates';

/**
 * Paginas que se recorren como mucho para resolver una imagen por su id (el logo del kit):
 * el API no publica GET /assets/{id}, y el logo suele ser de las imagenes recientes.
 */
const MAX_LOOKUP_PAGES = 10;

/** Busca una imagen de la empresa por id; null si no esta (o se retiro del listado). */
export async function findAsset(id: string): Promise<TemplateAsset | null> {
  let cursor: string | null = null;
  for (let page = 0; page < MAX_LOOKUP_PAGES; page += 1) {
    const result = await templatesApi.listAssets({ cursor });
    const found = result.items.find((asset) => asset.id === id);
    if (found) return found;
    if (!result.nextCursor) return null;
    cursor = result.nextCursor;
  }
  return null;
}
