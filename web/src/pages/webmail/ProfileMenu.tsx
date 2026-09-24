import { useEffect, useId, useRef, useState } from 'react';
import { Button } from '@/design/components';
import { IconLogOut, IconMoon, IconSettings, IconSun } from '@/design/icons';
import { t } from '@/i18n';
import { initialsOf } from './format';

export interface ProfileMenuProps {
  name: string;
  address: string;
  theme: 'light' | 'dark';
  signingOut: boolean;
  onToggleTheme: () => void;
  onSettings: () => void;
  onSignOut: () => void;
}

/** Cuenta del buzon: identidad, ajustes, tema y cierre de sesion bajo el avatar. */
export function ProfileMenu({
  name,
  address,
  theme,
  signingOut,
  onToggleTheme,
  onSettings,
  onSignOut,
}: ProfileMenuProps) {
  const [open, setOpen] = useState(false);
  const panelId = useId();
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const label = name || address;
  const initials = initialsOf(label);

  useEffect(() => {
    if (!open) return;
    const onPointer = (e: PointerEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return;
      setOpen(false);
      triggerRef.current?.focus();
    };
    document.addEventListener('pointerdown', onPointer);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('pointerdown', onPointer);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  const act = (action: () => void) => () => {
    setOpen(false);
    action();
  };

  return (
    <div className="cf-wm-profile" ref={rootRef}>
      <button
        ref={triggerRef}
        type="button"
        className="cf-wm-profile__trigger"
        aria-expanded={open}
        aria-controls={panelId}
        title={address}
        onClick={() => setOpen((v) => !v)}
      >
        <span className="cf-wm-avatar" aria-hidden="true">
          {initials}
        </span>
        <span className="cf-visually-hidden">{t('webmail.profile.open', { name: label })}</span>
      </button>
      {open ? (
        <div id={panelId} className="cf-wm-profile__panel">
          <p className="cf-wm-profile__address">{address}</p>
          <span className="cf-wm-avatar cf-wm-avatar--xl" aria-hidden="true">
            {initials}
          </span>
          <p className="cf-wm-profile__greeting">
            {t('webmail.profile.greeting', { name: label })}
          </p>
          <div className="cf-wm-profile__actions">
            <Button icon={<IconSettings size={16} />} onClick={act(onSettings)}>
              {t('webmail.settings.title')}
            </Button>
            <Button
              icon={theme === 'dark' ? <IconSun size={16} /> : <IconMoon size={16} />}
              onClick={act(onToggleTheme)}
            >
              {theme === 'dark' ? t('layout.theme.toLight') : t('layout.theme.toDark')}
            </Button>
            <Button icon={<IconLogOut size={16} />} loading={signingOut} onClick={onSignOut}>
              {t('webmail.logout')}
            </Button>
          </div>
        </div>
      ) : null}
    </div>
  );
}
