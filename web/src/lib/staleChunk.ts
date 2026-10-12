/*
 * Un despliegue renombra los archivos de la aplicacion (nombres con huella). Una pestana abierta
 * antes, o una cache vieja del navegador o del service worker, pide uno que ya no esta o que no
 * encaja con el resto, y el navegador responde con un error de carga de modulo. La salida
 * habitual es recargar una vez: la pagina nueva trae los nombres nuevos.
 */

const STORAGE_KEY = 'cfm:stale-chunk-reload';
// Dentro de esta ventana no se vuelve a recargar: si la pagina recien cargada falla igual, el
// problema no es una version vieja y recargar otra vez solo haria un bucle.
const RELOAD_WINDOW_MS = 60_000;

const CHUNK_ERROR =
  /Failed to fetch dynamically imported module|Importing a module script failed|error loading dynamically imported module|Unable to preload CSS/i;

/** Dice si el error es el de un modulo de la aplicacion que no se pudo cargar. */
export function isChunkLoadError(error: unknown): boolean {
  if (error instanceof Error) return CHUNK_ERROR.test(error.message);
  return typeof error === 'string' && CHUNK_ERROR.test(error);
}

export interface ReloadOnceOptions {
  now?: () => number;
  storage?: Pick<Storage, 'getItem' | 'setItem'>;
  reload?: () => void;
}

/**
 * Recarga la pagina si no lo hizo ya en el ultimo minuto y devuelve si lo hizo. Sin almacenamiento
 * de sesion no hay forma de saber si ya recargo, y entonces no recarga: preferible la pantalla de
 * error a un bucle.
 */
export function reloadOnceForStaleChunk(options: ReloadOnceOptions = {}): boolean {
  const now = options.now ?? Date.now;
  const reload = options.reload ?? (() => window.location.reload());
  try {
    const storage = options.storage ?? window.sessionStorage;
    const last = Number(storage.getItem(STORAGE_KEY));
    if (Number.isFinite(last) && last > 0 && now() - last < RELOAD_WINDOW_MS) return false;
    storage.setItem(STORAGE_KEY, String(now()));
  } catch {
    return false;
  }
  reload();
  return true;
}
