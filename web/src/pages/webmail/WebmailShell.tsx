import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { NavLink, Outlet, useLocation, useNavigate, useSearchParams } from 'react-router-dom';
import { FOLDER_ROLES, webmailApi, type WebmailFolder, type WebmailQuota } from '@/api/webmail';
import { useQuery } from '@/hooks/useQuery';
import { BrandMark } from '@/design/BrandMark';
import { Button, ErrorState, Meter, Modal, Skeleton, useToast } from '@/design/components';
import {
  IconAddressBook,
  IconBell,
  IconCalendar,
  IconEdit,
  IconKeyboard,
  IconLogOut,
  IconMail,
  IconMenu,
  IconMoon,
  IconSettings,
  IconSun,
} from '@/design/icons';
import { useTheme } from '@/design/useTheme';
import { formatBytes, usageRatio } from '@/lib/quota';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { watchInbox } from '@/webmail/events';
import {
  newUnread,
  notificationsSupported,
  notifyNewMail,
  readNotifyPreference,
  requestNotifyPermission,
  writeNotifyPreference,
} from '@/webmail/notifications';
import { SHORTCUTS, useShortcuts } from '@/webmail/shortcuts';
import { useWebmailStore } from '@/webmail/store';
import { FolderNav } from './FolderNav';
import { defaultFolder, folderWithRole } from './folders';
import type { WebmailOutlet } from './webmailContext';

function inboxUnread(folders: readonly WebmailFolder[] | null): number | null {
  if (!folders) return null;
  return folderWithRole(folders, FOLDER_ROLES.inbox)?.unread ?? null;
}

export function WebmailShell() {
  const session = useWebmailStore((s) => s.session);
  const logout = useWebmailStore((s) => s.logout);
  const toast = useToast();
  const { theme, toggle } = useTheme();
  const location = useLocation();
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [menuOpen, setMenuOpen] = useState(false);
  const [signingOut, setSigningOut] = useState(false);
  const [helpOpen, setHelpOpen] = useState(false);
  const [notify, setNotify] = useState(
    () =>
      notificationsSupported() && readNotifyPreference() && Notification.permission === 'granted',
  );

  const folders = useQuery((signal) => webmailApi.folders(signal), []);
  const { setData: setFolders, reload: reloadFolders } = folders;

  const adjustUnread = useCallback(
    (name: string, delta: number) =>
      setFolders((current) =>
        current
          ? current.map((f) =>
              f.name === name ? { ...f, unread: Math.max(0, f.unread + delta) } : f,
            )
          : current,
      ),
    [setFolders],
  );

  // Aviso de escritorio: los no leidos de la bandeja antes del aviso del servicio y despues
  // de volver a leer las carpetas. Solo cuenta lo que llega por el flujo, no lo que el propio
  // usuario marca como no leido.
  const unreadNow = useRef<number | null>(null);
  const unreadBeforeChange = useRef<number | null>(null);
  const notifyRef = useRef(notify);
  notifyRef.current = notify;
  useEffect(() => {
    const after = inboxUnread(folders.data);
    const before = unreadBeforeChange.current;
    if (before !== null && after !== null && !folders.loading) {
      unreadBeforeChange.current = null;
      if (notifyRef.current) {
        notifyNewMail(newUnread(before, after), () => {
          const inbox = folders.data ? defaultFolder(folders.data) : null;
          navigate(inbox ? paths.webmailView({ folder: inbox }) : paths.webmail);
        });
      }
    }
    unreadNow.current = after;
  }, [folders.data, folders.loading, navigate]);

  const [inboxTick, setInboxTick] = useState(0);
  const refreshSession = useWebmailStore((s) => s.refresh);
  useEffect(
    () =>
      watchInbox({
        onChange: () => {
          unreadBeforeChange.current ??= unreadNow.current;
          reloadFolders();
          setInboxTick((tick) => tick + 1);
        },
        // Una lectura de la sesion responde SESSION_EXPIRED y el store vuelve a la pantalla de acceso.
        onSessionExpired: () => void refreshSession(),
      }),
    [reloadFolders, refreshSession],
  );
  const outlet = useMemo<WebmailOutlet>(
    () => ({ folders, adjustUnread, reloadFolders, inboxTick }),
    [folders, adjustUnread, reloadFolders, inboxTick],
  );

  useEffect(() => {
    setMenuOpen(false);
  }, [location.pathname, location.search]);

  useShortcuts({
    c: () => navigate(paths.webmailCompose),
    '?': () => setHelpOpen(true),
  });

  const onMailbox = location.pathname === paths.webmail;
  const current = onMailbox
    ? (params.get('folder') ?? (folders.data ? defaultFolder(folders.data) : null))
    : location.pathname === paths.webmailScheduled && folders.data
      ? (folderWithRole(folders.data, FOLDER_ROLES.scheduled)?.name ?? null)
      : null;

  const signOut = async () => {
    setSigningOut(true);
    try {
      await logout();
    } catch {
      toast.error(t('webmail.logoutFailed'));
      setSigningOut(false);
    }
  };

  const toggleNotify = async () => {
    if (notify) {
      writeNotifyPreference(false);
      setNotify(false);
      toast.info(t('webmail.notify.off'));
      return;
    }
    // El permiso se pide aqui, por la accion del usuario, nunca al cargar.
    const granted = await requestNotifyPermission();
    if (!granted) {
      toast.error(t('webmail.notify.denied'));
      return;
    }
    writeNotifyPreference(true);
    setNotify(true);
    toast.success(t('webmail.notify.on'));
  };

  const appLink = (to: string, label: string, icon: ReactNode, end = false) => (
    <li>
      <NavLink to={to} end={end} className="cf-wm-apps__link">
        {icon}
        <span>{label}</span>
      </NavLink>
    </li>
  );

  return (
    <div className="cf-wm">
      <a className="cf-wm-skip" href="#wm-main">
        {t('webmail.skipToContent')}
      </a>
      <header className="cf-wm__topbar">
        <div className="cf-wm__brand">
          <Button
            variant="ghost"
            iconOnly
            className="cf-wm__menu-toggle"
            icon={<IconMenu size={18} />}
            aria-expanded={menuOpen}
            aria-controls="wm-sidebar"
            onClick={() => setMenuOpen((v) => !v)}
          >
            {t('webmail.folders.title')}
          </Button>
          <BrandMark />
          <span className="cf-wm__product">{t('webmail.title')}</span>
        </div>
        <div className="cf-wm__account">
          {session ? (
            <div className="cf-wm__identity">
              <span className="cf-wm__identity-name">
                {session.display_name || session.username}
              </span>
              {session.display_name ? (
                <span className="cf-wm__identity-address">{session.username}</span>
              ) : null}
            </div>
          ) : null}
          {notificationsSupported() ? (
            <Button
              variant="ghost"
              iconOnly
              className="cf-wm-notify"
              aria-pressed={notify}
              icon={<IconBell size={18} />}
              onClick={() => void toggleNotify()}
            >
              {t(notify ? 'webmail.notify.disable' : 'webmail.notify.enable')}
            </Button>
          ) : null}
          <Button
            variant="ghost"
            iconOnly
            className="cf-wm__help"
            icon={<IconKeyboard size={18} />}
            onClick={() => setHelpOpen(true)}
          >
            {t('webmail.shortcuts.title')}
          </Button>
          <Button
            variant="ghost"
            iconOnly
            icon={theme === 'dark' ? <IconSun size={18} /> : <IconMoon size={18} />}
            onClick={toggle}
          >
            {theme === 'dark' ? t('layout.theme.toLight') : t('layout.theme.toDark')}
          </Button>
          <Button
            variant="ghost"
            iconOnly
            icon={<IconLogOut size={18} />}
            loading={signingOut}
            onClick={() => void signOut()}
          >
            {t('webmail.logout')}
          </Button>
        </div>
      </header>
      <aside
        id="wm-sidebar"
        className={['cf-wm__sidebar', menuOpen ? 'cf-wm__sidebar--open' : '']
          .filter(Boolean)
          .join(' ')}
      >
        <Button
          variant="primary"
          block
          icon={<IconEdit size={16} />}
          onClick={() => navigate(paths.webmailCompose)}
        >
          {t('webmail.compose.new')}
        </Button>
        <nav aria-label={t('webmail.apps.label')}>
          <ul className="cf-wm-apps">
            {appLink(paths.webmail, t('webmail.apps.mail'), <IconMail size={16} />, true)}
            {appLink(
              paths.webmailContacts,
              t('webmail.apps.contacts'),
              <IconAddressBook size={16} />,
            )}
            {appLink(paths.webmailCalendar, t('webmail.apps.calendar'), <IconCalendar size={16} />)}
          </ul>
        </nav>
        {folders.error && !folders.data ? (
          <ErrorState error={folders.error} onRetry={reloadFolders} />
        ) : folders.data ? (
          <FolderNav folders={folders.data} current={current} onChanged={reloadFolders} />
        ) : (
          <Skeleton lines={6} />
        )}
        <div className="cf-wm__sidebar-foot">
          <Button
            variant="ghost"
            block
            icon={<IconSettings size={16} />}
            onClick={() => navigate(paths.webmailSettings)}
          >
            {t('webmail.settings.open')}
          </Button>
          <QuotaSummary quota={session?.quota ?? null} />
        </div>
      </aside>
      <div
        className={['cf-wm__backdrop', menuOpen ? 'cf-wm__backdrop--visible' : '']
          .filter(Boolean)
          .join(' ')}
        onClick={() => setMenuOpen(false)}
        aria-hidden="true"
      />
      <main className="cf-wm__main" id="wm-main" tabIndex={-1}>
        <Outlet context={outlet} />
      </main>
      <Modal
        open={helpOpen}
        title={t('webmail.shortcuts.title')}
        onClose={() => setHelpOpen(false)}
      >
        <dl className="cf-wm-shortcuts">
          {SHORTCUTS.map((shortcut) => (
            <div key={shortcut.keys} className="cf-wm-shortcuts__row">
              <dt>
                <kbd>{shortcut.keys}</kbd>
              </dt>
              <dd>{t(shortcut.label)}</dd>
            </div>
          ))}
        </dl>
        <p className="cf-field__hint">{t('webmail.shortcuts.hint')}</p>
      </Modal>
    </div>
  );
}

function QuotaSummary({ quota }: { quota: WebmailQuota | null }) {
  if (!quota) return null;
  const ratio = usageRatio(quota.used_bytes, quota.limit_bytes);
  return (
    <div className="cf-wm-quota">
      <span>
        {quota.limit_bytes > 0
          ? t('webmail.quota.usage', {
              used: formatBytes(quota.used_bytes),
              limit: formatBytes(quota.limit_bytes),
            })
          : t('webmail.quota.used', { used: formatBytes(quota.used_bytes) })}
      </span>
      {ratio !== null ? <Meter ratio={ratio} label={t('webmail.quota.label')} /> : null}
    </div>
  );
}
