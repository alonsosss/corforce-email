import { openWebmailEvents } from '@/api/webmail';

/*
 * Avisos de la bandeja en tiempo real, con red de seguridad.
 *
 * El servicio mantiene un flujo SSE con los cambios de INBOX (un correo nuevo, uno borrado, otro leido en otro
 * cliente). Si el flujo no llega a abrirse o se cierra para siempre (sesion, tope de conexiones, servicio sin
 * avisos), la bandeja se refresca por sondeo, solo con la pestana visible. Al volver a la pestana tambien se
 * refresca una vez. Al reabrirse el flujo tras un corte se refresca, porque los avisos perdidos no se repiten.
 */

/** EventSource.CLOSED (2): el navegador no volvera a reconectar por su cuenta. */
const EVENT_SOURCE_CLOSED = 2;

/** Cada cuanto se sondea con la pestana visible mientras no hay flujo. */
export const POLL_INTERVAL_MS = 45_000;
/** Los avisos que llegan casi juntos (varios correos seguidos) se funden en un solo refresco. */
export const DEBOUNCE_MS = 300;
/** Tras cerrarse el flujo para siempre, cuanto se espera para volver a intentar abrirlo. */
export const REOPEN_AFTER_MS = 60_000;
/** Las vueltas a la pestana no refrescan mas de una vez en este margen. */
export const VISIBILITY_THROTTLE_MS = 5_000;

export interface InboxWatchHandlers {
  /** Hay que volver a leer la bandeja y las carpetas. */
  onChange: () => void;
  /** El servicio dijo que la sesion cayo. */
  onSessionExpired: () => void;
}

/** Empieza a vigilar y devuelve la funcion que lo detiene todo (flujo, temporizadores y escuchas). */
export function watchInbox({ onChange, onSessionExpired }: InboxWatchHandlers): () => void {
  let source: EventSource | null = null;
  let stopped = false;
  let connected = false;
  let everConnected = false;
  let debounce: ReturnType<typeof setTimeout> | null = null;
  let poll: ReturnType<typeof setInterval> | null = null;
  let reopen: ReturnType<typeof setTimeout> | null = null;
  let lastVisibleRefresh = 0;

  const refreshSoon = () => {
    if (debounce) return;
    debounce = setTimeout(() => {
      debounce = null;
      if (!stopped) onChange();
    }, DEBOUNCE_MS);
  };

  const visible = () => typeof document === 'undefined' || document.visibilityState === 'visible';

  const startPolling = () => {
    if (poll || stopped) return;
    poll = setInterval(() => {
      if (visible()) onChange();
    }, POLL_INTERVAL_MS);
  };
  const stopPolling = () => {
    if (poll) clearInterval(poll);
    poll = null;
  };

  const close = () => {
    if (source) {
      source.close();
      source = null;
    }
    connected = false;
  };

  const open = () => {
    if (stopped) return;
    close();
    if (typeof EventSource === 'undefined') {
      // Un navegador sin Server-Sent Events: solo sondeo.
      startPolling();
      return;
    }
    const es = openWebmailEvents();
    source = es;
    es.addEventListener('ready', () => {
      connected = true;
      stopPolling();
      // Tras un corte se pudieron perder avisos: se lee de nuevo.
      if (everConnected) refreshSoon();
      everConnected = true;
    });
    es.addEventListener('mailbox', refreshSoon);
    es.addEventListener('session-expired', () => {
      close();
      stopPolling();
      onSessionExpired();
    });
    es.addEventListener('reconnect', () => {
      // El servicio cierra el flujo a proposito; EventSource reconecta solo.
      connected = false;
    });
    es.onerror = () => {
      connected = false;
      if (es.readyState === EVENT_SOURCE_CLOSED) {
        // Rechazado (sesion, tope, avisos desactivados): no reconecta solo. Se sondea y se reintenta luego.
        close();
        startPolling();
        if (!reopen && !stopped) {
          reopen = setTimeout(() => {
            reopen = null;
            open();
          }, REOPEN_AFTER_MS);
        }
      } else {
        // Reconectando: mientras tanto, red de seguridad.
        startPolling();
      }
    };
  };

  const onVisibility = () => {
    if (!visible() || stopped) return;
    const now = Date.now();
    if (now - lastVisibleRefresh < VISIBILITY_THROTTLE_MS) return;
    lastVisibleRefresh = now;
    if (!connected) onChange();
  };
  document.addEventListener('visibilitychange', onVisibility);

  open();

  return () => {
    stopped = true;
    document.removeEventListener('visibilitychange', onVisibility);
    if (debounce) clearTimeout(debounce);
    if (reopen) clearTimeout(reopen);
    stopPolling();
    close();
  };
}
