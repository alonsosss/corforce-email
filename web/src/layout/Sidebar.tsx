import { useEffect, useRef } from 'react';
import { NavLink, useLocation } from 'react-router-dom';
import { useAccess } from '@/access/useAccess';
import { visibleNav } from './nav';
import { t } from '@/i18n';
import { paths } from '@/paths';

export interface SidebarProps {
  open: boolean;
  onNavigate: () => void;
}

export function Sidebar({ open, onNavigate }: SidebarProps) {
  const access = useAccess();
  const groups = visibleNav(access);
  const navRef = useRef<HTMLElement>(null);
  const { pathname } = useLocation();

  // El menu es mas alto que la pantalla: una opcion activa de los grupos de abajo (Marketing,
  // Plataforma) quedaba fuera de la vista y no se sabia donde estaba uno.
  useEffect(() => {
    navRef.current?.querySelector('.cf-nav-link.active')?.scrollIntoView?.({ block: 'nearest' });
  }, [pathname, groups.length]);

  return (
    <aside
      id="cf-sidebar"
      className={['cf-sidebar', open ? 'cf-sidebar--open' : ''].filter(Boolean).join(' ')}
    >
      <nav ref={navRef} className="cf-sidebar__nav" aria-label={t('app.tagline')}>
        {groups.map((group) => (
          <div className="cf-nav-group" key={group.labelKey}>
            <div className="cf-nav-group__label">{t(group.labelKey)}</div>
            <ul>
              {group.items.map((item) => {
                const Icon = item.icon;
                return (
                  <li key={item.to}>
                    <NavLink
                      to={item.to}
                      end={item.to === paths.home}
                      className="cf-nav-link"
                      onClick={onNavigate}
                    >
                      <Icon size={20} />
                      <span>{t(item.labelKey)}</span>
                    </NavLink>
                  </li>
                );
              })}
            </ul>
          </div>
        ))}
      </nav>
    </aside>
  );
}
