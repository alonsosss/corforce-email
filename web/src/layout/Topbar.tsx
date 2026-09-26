import { useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useAuth } from '@/auth/useAuth';
import { useAccess } from '@/access/useAccess';
import { useTheme } from '@/design/useTheme';
import { BrandMark } from '@/design/BrandMark';
import { Button, ProfileMenu } from '@/design/components';
import { IconMenu } from '@/design/icons';
import { fullName } from '@/lib/format';
import { t } from '@/i18n';
import { paths } from '@/paths';

export interface TopbarProps {
  menuOpen: boolean;
  onToggleMenu: () => void;
}

export function Topbar({ menuOpen, onToggleMenu }: TopbarProps) {
  const { user, logout } = useAuth();
  const { roles } = useAccess();
  const { theme, toggle } = useTheme();
  const navigate = useNavigate();
  const [signingOut, setSigningOut] = useState(false);

  const signOut = () => {
    setSigningOut(true);
    void logout();
  };

  return (
    <header className="cf-topbar">
      <div className="cf-topbar__brand">
        <Button
          variant="ghost"
          iconOnly
          className="cf-topbar__menu-toggle"
          icon={<IconMenu size={18} />}
          aria-expanded={menuOpen}
          aria-controls="cf-sidebar"
          onClick={onToggleMenu}
        >
          {t('layout.openMenu')}
        </Button>
        <Link to={paths.home} className="cf-topbar__home">
          <BrandMark />
        </Link>
      </div>
      <div className="cf-topbar__right">
        {user ? (
          <ProfileMenu
            name={fullName(user.first_name, user.last_name, user.email)}
            address={user.email}
            detail={roles.join(', ')}
            theme={theme}
            signingOut={signingOut}
            settingsLabel={t('nav.account')}
            signOutLabel={t('layout.logout')}
            onToggleTheme={toggle}
            onSettings={() => navigate(paths.account)}
            onSignOut={signOut}
          />
        ) : null}
      </div>
    </header>
  );
}
