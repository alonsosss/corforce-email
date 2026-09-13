/** Recurso de solo lectura que no cambia durante la sesion (catalogos del dominio). */
export interface CachedResource<T> {
  get: () => Promise<T>;
}

/** Recurso ligado a una sesion concreta: se descarta cuando esa sesion termina. */
export interface SessionResource<T> extends CachedResource<T> {
  reset: () => void;
}

/**
 * Una sola peticion compartida por todas las pantallas que lo piden. Si falla no se
 * guarda nada: la siguiente lectura vuelve a intentarlo.
 */
export function cachedResource<T>(load: () => Promise<T>): CachedResource<T> {
  const { get } = sessionResource(load);
  return { get };
}

/**
 * Como cachedResource, pero reset() lo descarta: la siguiente lectura pide de nuevo. Una
 * peticion de antes del reset que falle no borra la de despues.
 */
export function sessionResource<T>(load: () => Promise<T>): SessionResource<T> {
  let pending: Promise<T> | null = null;
  return {
    get: () => {
      if (!pending) {
        const current: Promise<T> = load().catch((err: unknown) => {
          if (pending === current) pending = null;
          throw err;
        });
        pending = current;
      }
      return pending;
    },
    reset: () => {
      pending = null;
    },
  };
}
