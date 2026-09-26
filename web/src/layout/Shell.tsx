import { useCallback, useState } from 'react';
import { Outlet } from 'react-router-dom';
import { Sidebar } from './Sidebar';
import { Topbar } from './Topbar';
import { StepUpModal } from '@/auth/StepUpModal';
import './layout.css';

export function Shell() {
  const [menuOpen, setMenuOpen] = useState(false);
  const closeMenu = useCallback(() => setMenuOpen(false), []);

  return (
    <div className="cf-shell">
      <Sidebar open={menuOpen} onNavigate={closeMenu} />
      <div
        className={['cf-sidebar-backdrop', menuOpen ? 'cf-sidebar-backdrop--visible' : '']
          .filter(Boolean)
          .join(' ')}
        onClick={closeMenu}
        aria-hidden="true"
      />
      <Topbar menuOpen={menuOpen} onToggleMenu={() => setMenuOpen((v) => !v)} />
      <main className="cf-main" id="main">
        <div className="cf-main__surface">
          <Outlet />
        </div>
      </main>
      <StepUpModal />
    </div>
  );
}
