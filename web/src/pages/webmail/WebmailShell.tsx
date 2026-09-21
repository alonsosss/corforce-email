import { useCallback, useEffect, useMemo, useState } from 'react';
import { Outlet, useLocation, useNavigate, useSearchParams } from 'react-router-dom';
import { webmailApi, type WebmailQuota } from '@/api/webmail';
import { useQuery } from '@/hooks/useQuery';
import { BrandMark } from '@/design/BrandMark';
import { Button, ErrorState, Meter, Skeleton, useToast } from '@/design/components';
import { IconCalendar, IconEdit, IconLogOut, IconMenu, IconMoon, IconSun } from '@/design/icons';
import { useTheme } from '@/design/useTheme';
import { formatBytes, usageRatio } from '@/lib/quota';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { watchInbox } from '@/webmail/events';
import { useWebmailStore } from '@/webmail/store';
import { FolderNav } from './FolderNav';
import { defaultFolder } from './folders';
import type { WebmailOutlet } from './webmailContext';

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
  const [inboxTick, setInboxTick] = useState(0);
  const refreshSession = useWebmailStore((s) => s.refresh);
  useEffect(
    () =>
      watchInbox({
        onChange: () => {
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

  const onMailbox = location.pathname === paths.webmail;
  const current = onMailbox
    ? (params.get('folder') ?? (folders.data ? defaultFolder(folders.data) : null))
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
        <Button
          variant="ghost"
          block
          icon={<IconCalendar size={16} />}
          onClick={() => navigate(paths.webmailSettings)}
        >
          {t('webmail.settings.open')}
        </Button>
        {folders.error && !folders.data ? (
          <ErrorState error={folders.error} onRetry={reloadFolders} />
        ) : folders.data ? (
          <FolderNav folders={folders.data} current={current} />
        ) : (
          <Skeleton lines={6} />
        )}
        <QuotaSummary quota={session?.quota ?? null} />
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
