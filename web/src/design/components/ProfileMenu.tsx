import { useEffect, useId, useRef, useState } from 'react';
import { IconLogOut, IconMoon, IconSettings, IconSun } from '@/design/icons';
import { t } from '@/i18n';
import { initialsOf } from '@/lib/format';
import { Button } from './Button';

export interface ProfileMenuProps {
  name: string;
  /** Direccion o correo que identifica la cuenta; tambien da las iniciales si no hay nombre. */
  address: string;
  /** Linea secundaria bajo el saludo, por ejemplo los roles de la cuenta. */
  detail?: string;
  theme: 'light' | 'dark';
  signingOut: boolean;
  settingsLabel: string;
  signOutLabel: string;
  onToggleTheme: () => void;
  onSettings: () => void;
  onSignOut: () => void;
}

/** Cuenta de la sesion: identidad, ajustes, tema y cierre de sesion bajo el avatar. */
export function ProfileMenu({
  name,
  address,
  detail,
  theme,
  signingOut,
  settingsLabel,
  signOutLabel,
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
    <div className="cf-profile" ref={rootRef}>
      <button
        ref={triggerRef}
        type="button"
        className="cf-profile__trigger"
        aria-expanded={open}
        aria-controls={panelId}
        title={address}
        onClick={() => setOpen((v) => !v)}
      >
        <span className="cf-avatar" aria-hidden="true">
          {initials}
        </span>
        <span className="cf-visually-hidden">{t('profile.open', { name: label })}</span>
      </button>
      {open ? (
        <div id={panelId} className="cf-profile__panel">
          <p className="cf-profile__address">{address}</p>
          <span className="cf-avatar cf-avatar--xl" aria-hidden="true">
            {initials}
          </span>
          <p className="cf-profile__greeting">{t('profile.greeting', { name: label })}</p>
          {detail ? <p className="cf-profile__detail">{detail}</p> : null}
          <div className="cf-profile__actions">
            <Button icon={<IconSettings size={16} />} onClick={act(onSettings)}>
              {settingsLabel}
            </Button>
            <Button
              icon={theme === 'dark' ? <IconSun size={16} /> : <IconMoon size={16} />}
              onClick={act(onToggleTheme)}
            >
              {theme === 'dark' ? t('layout.theme.toLight') : t('layout.theme.toDark')}
            </Button>
            <Button icon={<IconLogOut size={16} />} loading={signingOut} onClick={onSignOut}>
              {signOutLabel}
            </Button>
          </div>
        </div>
      ) : null}
    </div>
  );
}
