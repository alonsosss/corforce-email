import { useNavigate } from 'react-router-dom';
import { useAuth } from '@/auth/useAuth';
import { useAccess } from '@/access/useAccess';
import { useTheme } from '@/design/useTheme';
import { Button } from '@/design/components';
import { IconLogOut, IconMenu, IconMoon, IconSun, IconUser } from '@/design/icons';
import { fullName } from '@/lib/format';
import { t } from '@/i18n';
import { paths } from '@/paths';

export interface TopbarProps {
  onToggleMenu: () => void;
}

export function Topbar({ onToggleMenu }: TopbarProps) {
  const { user, logout } = useAuth();
  const { roles } = useAccess();
  const { theme, toggle } = useTheme();
  const navigate = useNavigate();

  const name = user ? fullName(user.first_name, user.last_name, user.email) : '';

  return (
    <header className="cf-topbar">
      <div className="cf-topbar__left">
        <Button
          variant="ghost"
          iconOnly
          className="cf-topbar__menu-toggle"
          icon={<IconMenu size={18} />}
          onClick={onToggleMenu}
        >
          {t('layout.openMenu')}
        </Button>
      </div>
      <div className="cf-topbar__right">
        {user ? (
          <div className="cf-topbar__user">
            <span className="cf-topbar__user-name">{name}</span>
            {roles.length ? <span className="cf-topbar__user-role">{roles.join(', ')}</span> : null}
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
          icon={<IconUser size={18} />}
          onClick={() => navigate(paths.account)}
        >
          {t('nav.account')}
        </Button>
        <Button
          variant="ghost"
          iconOnly
          icon={<IconLogOut size={18} />}
          onClick={() => void logout()}
        >
          {t('layout.logout')}
        </Button>
      </div>
    </header>
  );
}
