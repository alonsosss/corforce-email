import { useCallback } from 'react';
import { useSearchParams } from 'react-router-dom';

/**
 * Pestana activa guardada en ?tab= para que se pueda enlazar y sobreviva a recargar. Solo
 * vale una pestana disponible (las demas se ocultan por permiso); si no, la de reserva.
 */
export function useTabParam<K extends string>(
  available: readonly K[],
  fallback: K,
): [K, (next: K) => void] {
  const [params, setParams] = useSearchParams();
  const raw = params.get('tab');
  const current =
    available.find((id) => id === raw) ??
    (available.includes(fallback) ? fallback : (available[0] ?? fallback));

  const select = useCallback(
    (next: K) => {
      setParams((prev) => {
        const out = new URLSearchParams(prev);
        out.set('tab', next);
        return out;
      });
    },
    [setParams],
  );

  return [current, select];
}
