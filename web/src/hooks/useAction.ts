import { useCallback, useState } from 'react';

export interface ActionState<A extends unknown[]> {
  run: (...args: A) => Promise<boolean>;
  busy: boolean;
  error: unknown;
  clearError: () => void;
}

/**
 * Envuelve una mutacion: expone busy y error, y devuelve true si termino bien. Las
 * pantallas encadenan el aviso de exito sin repetir el try/catch.
 */
export function useAction<A extends unknown[]>(
  action: (...args: A) => Promise<unknown>,
): ActionState<A> {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);

  const run = useCallback(
    async (...args: A): Promise<boolean> => {
      setBusy(true);
      setError(null);
      try {
        await action(...args);
        return true;
      } catch (err) {
        setError(err);
        return false;
      } finally {
        setBusy(false);
      }
    },
    [action],
  );

  const clearError = useCallback(() => setError(null), []);

  return { run, busy, error, clearError };
}
