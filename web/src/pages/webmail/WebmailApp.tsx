import { useEffect } from 'react';
import { Navigate, Outlet, Route, Routes, useLocation } from 'react-router-dom';
import { ErrorState, LoadingBlock } from '@/design/components';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { useWebmailStore } from '@/webmail/store';
import ComposeRoute from './ComposeWindow';
import MailboxPage from './MailboxPage';
import CalendarPage from './calendar/CalendarPage';
import ContactsPage from './contacts/ContactsPage';
import ScheduledPage from './ScheduledPage';
import SnoozedPage from './SnoozedPage';
import SettingsPage from './SettingsPage';
import { WebmailLoginPage } from './WebmailLoginPage';
import { WebmailShell } from './WebmailShell';
import './webmail.css';

/**
 * Aplicacion del webmail: su propia sesion (cookie cf_wm del servicio), su propio marco y
 * sus propias rutas bajo /webmail. No usa la sesion ni el layout de la plataforma.
 */
export default function WebmailApp() {
  useEffect(() => {
    void useWebmailStore.getState().check();
  }, []);

  return (
    <Routes>
      <Route path="login" element={<WebmailLoginPage />} />
      <Route element={<RequireWebmailSession />}>
        <Route element={<WebmailShell />}>
          <Route index element={<MailboxPage />} />
          <Route path="compose" element={<ComposeRoute />} />
          <Route path="settings" element={<SettingsPage />} />
          <Route path="scheduled" element={<ScheduledPage />} />
          <Route path="snoozed" element={<SnoozedPage />} />
          <Route path="contacts" element={<ContactsPage />} />
          <Route path="calendar" element={<CalendarPage />} />
        </Route>
      </Route>
      <Route path="*" element={<Navigate to={paths.webmail} replace />} />
    </Routes>
  );
}

/**
 * Mientras se comprueba la cookie no se decide nada. Sin sesion se vuelve al acceso recordando la
 * pantalla, salvo si el usuario cerro la sesion: quien entre despues puede ser otro buzon, y un
 * mensaje abierto no existe en el suyo.
 */
function RequireWebmailSession() {
  const status = useWebmailStore((s) => s.status);
  const checkError = useWebmailStore((s) => s.checkError);
  const closedByUser = useWebmailStore((s) => s.closedByUser);
  const location = useLocation();

  if (status === 'checking') {
    return (
      <div className="cf-fullscreen">
        <LoadingBlock label={t('webmail.checking')} />
      </div>
    );
  }
  if (status === 'unavailable') {
    return (
      <div className="cf-fullscreen">
        <ErrorState
          error={checkError}
          title={t('webmail.unavailable')}
          onRetry={() => void useWebmailStore.getState().check()}
        />
      </div>
    );
  }
  if (status !== 'authenticated') {
    return (
      <Navigate
        to={paths.webmailLogin}
        replace
        state={closedByUser ? undefined : { from: `${location.pathname}${location.search}` }}
      />
    );
  }
  return <Outlet />;
}
