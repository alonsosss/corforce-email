import { useEffect, useRef, useState } from 'react';
import { templatesApi, type CheckRequest, type CheckResult } from '@/api/templates';

/** Espera tras el ultimo cambio antes de verificar: no se llama al servicio en cada tecla. */
export const CHECK_DEBOUNCE_MS = 1500;

export interface DeliverabilityState {
  result: CheckResult | null;
  error: unknown;
  checking: boolean;
}

/**
 * Verificacion en vivo con POST /templates/check. `revision` cambia con cada edicion; tras
 * CHECK_DEBOUNCE_MS sin cambios se construye el contenido (compilar MJML cuesta) y se
 * verifica. Una verificacion en vuelo se cancela si llega otra edicion.
 */
export function useDeliverabilityCheck(
  revision: number,
  build: () => CheckRequest | null,
  enabled: boolean,
): DeliverabilityState {
  const [state, setState] = useState<DeliverabilityState>({
    result: null,
    error: null,
    checking: false,
  });
  const buildRef = useRef(build);
  buildRef.current = build;

  useEffect(() => {
    if (!enabled) return;
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      const request = buildRef.current();
      if (!request) return;
      setState((prev) => ({ ...prev, checking: true }));
      templatesApi
        .check(request, controller.signal)
        .then((result) => {
          if (!controller.signal.aborted) setState({ result, error: null, checking: false });
        })
        .catch((error: unknown) => {
          if (!controller.signal.aborted) setState((prev) => ({ ...prev, error, checking: false }));
        });
    }, CHECK_DEBOUNCE_MS);
    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [revision, enabled]);

  return state;
}
