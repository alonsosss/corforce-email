/*
 * Registro del service worker (web/public/sw.js), que solo hace la aplicacion instalable y guarda la
 * carcasa estatica: nunca el API ni el documento, que el gateway sirve con un nonce nuevo en la CSP.
 * Solo en produccion y en un contexto seguro; si el navegador o la politica lo impiden, la
 * aplicacion funciona igual sin instalarse.
 */
export const SERVICE_WORKER_URL = '/sw.js';

export interface ServiceWorkerEnv {
  production: boolean;
  secure: boolean;
  container: Pick<ServiceWorkerContainer, 'register'> | undefined;
}

export function currentServiceWorkerEnv(): ServiceWorkerEnv {
  return {
    production: import.meta.env.PROD,
    secure: typeof window !== 'undefined' && window.isSecureContext,
    container:
      typeof navigator !== 'undefined' && 'serviceWorker' in navigator
        ? navigator.serviceWorker
        : undefined,
  };
}

/** Registra el service worker cuando la pagina termina de cargar. Devuelve si lo intento. */
export function registerServiceWorker(env: ServiceWorkerEnv = currentServiceWorkerEnv()): boolean {
  const { container } = env;
  if (!env.production || !env.secure || !container) return false;
  const register = () => {
    container
      .register(SERVICE_WORKER_URL, { scope: '/', updateViaCache: 'none' })
      .catch((err: unknown) => console.warn('No se pudo registrar el service worker', err));
  };
  if (document.readyState === 'complete') register();
  else window.addEventListener('load', register, { once: true });
  return true;
}
