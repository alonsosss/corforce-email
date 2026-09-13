import { useEffect } from 'react';
import { Outlet } from 'react-router-dom';
import { useAuthStore } from '@/auth/store';

let hydrationStarted = false;

/**
 * Restaura la sesion de la plataforma (cookie cf_rt) una vez por carga de pagina y solo en
 * sus rutas. El webmail queda fuera: es otra sesion y nunca pide /api/v1/auth.
 */
export function PlatformSession() {
  useEffect(() => {
    if (hydrationStarted) return;
    hydrationStarted = true;
    void useAuthStore.getState().hydrate();
  }, []);
  return <Outlet />;
}
