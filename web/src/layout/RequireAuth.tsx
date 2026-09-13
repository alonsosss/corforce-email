import { Navigate, Outlet, useLocation } from 'react-router-dom';
import { useAuth } from '@/auth/useAuth';
import { paths } from '@/paths';
import { LoadingBlock } from '@/design/components';
import { t } from '@/i18n';

/**
 * Mientras se comprueba la cookie (hydrating) no se decide nada: tratar "no se sabe"
 * como "no hay sesion" es lo que hace parpadear el login en cada recarga.
 */
export function RequireAuth() {
  const { status, sessionExpired } = useAuth();
  const location = useLocation();

  if (status === 'hydrating') {
    return (
      <div className="cf-fullscreen">
        <LoadingBlock label={t('auth.hydrating')} />
      </div>
    );
  }
  if (status !== 'authenticated') {
    if (sessionExpired) return <Navigate to={paths.sessionExpired} replace />;
    return <Navigate to={paths.login} replace state={{ from: location.pathname }} />;
  }
  return <Outlet />;
}
