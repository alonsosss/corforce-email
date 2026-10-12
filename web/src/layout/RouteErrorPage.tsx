import { useEffect } from 'react';
import { useRouteError } from 'react-router-dom';
import { AuthLayout } from '@/pages/auth/AuthLayout';
import { t } from '@/i18n';
import { isChunkLoadError, reloadOnceForStaleChunk } from '@/lib/staleChunk';

/**
 * Lo que ve el usuario cuando una pantalla falla al cargarse o al dibujarse. Reemplaza la pagina de
 * error por defecto del enrutador, que muestra el error tecnico. Un archivo de la aplicacion que no
 * carga casi siempre es una version vieja en una pestana abierta: se recarga una vez sola.
 */
export function RouteErrorPage() {
  const error = useRouteError();
  const stale = isChunkLoadError(error);

  useEffect(() => {
    if (stale) reloadOnceForStaleChunk();
    else console.error(error);
  }, [error, stale]);

  return (
    <AuthLayout title={t('app.routeError.title')} subtitle={t('app.routeError.description')}>
      <button
        type="button"
        className="cf-btn cf-btn--primary cf-btn--block"
        onClick={() => window.location.reload()}
      >
        {t('app.routeError.reload')}
      </button>
      <a className="cf-btn cf-btn--ghost cf-btn--block" href="/">
        {t('app.routeError.home')}
      </a>
    </AuthLayout>
  );
}
