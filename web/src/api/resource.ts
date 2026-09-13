/** Recurso de solo lectura que no cambia durante la sesion (catalogos del dominio). */
export interface CachedResource<T> {
  get: () => Promise<T>;
}

/**
 * Una sola peticion compartida por todas las pantallas que lo piden. Si falla no se
 * guarda nada: la siguiente lectura vuelve a intentarlo.
 */
export function cachedResource<T>(load: () => Promise<T>): CachedResource<T> {
  let pending: Promise<T> | null = null;
  return {
    get: () => {
      pending ??= load().catch((err: unknown) => {
        pending = null;
        throw err;
      });
      return pending;
    },
  };
}
