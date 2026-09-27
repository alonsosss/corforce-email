import type { ComponentType, ReactNode } from 'react';
import { BrandLogo } from '@/design/BrandLogo';
import {
  IconCalendar,
  IconFileText,
  IconMail,
  IconShieldCheck,
  IconUser,
  IconUsers,
  type IconProps,
} from '@/design/icons';
import { t, type MessageKey } from '@/i18n';
import '@/layout/layout.css';

export interface AuthLayoutProps {
  title: string;
  subtitle?: string;
  children: ReactNode;
  /** Enlace a la otra entrada (plataforma o correo web), bajo el formulario. */
  footer?: ReactNode;
}

interface HeroItem {
  icon: ComponentType<IconProps>;
  title: MessageKey;
  text?: MessageKey;
}

const FEATURES: readonly HeroItem[] = [
  { icon: IconMail, title: 'auth.hero.secure.title', text: 'auth.hero.secure.text' },
  { icon: IconUsers, title: 'auth.hero.contacts.title', text: 'auth.hero.contacts.text' },
  { icon: IconFileText, title: 'auth.hero.files.title', text: 'auth.hero.files.text' },
];

const CHIPS: readonly HeroItem[] = [
  { icon: IconUser, title: 'auth.hero.chip.contacts' },
  { icon: IconCalendar, title: 'auth.hero.chip.calendar' },
  { icon: IconFileText, title: 'auth.hero.chip.files' },
];

export function AuthLayout({ title, subtitle, children, footer }: AuthLayoutProps) {
  return (
    <main className="cf-auth">
      <section className="cf-auth__hero" aria-label={t('app.name')}>
        <div className="cf-auth__hero-brand">
          <BrandLogo size={52} />
          <span>{t('app.name')}</span>
        </div>
        <p className="cf-auth__headline">
          {t('auth.hero.headline')}
          <br />
          <span className="cf-auth__headline-accent">{t('auth.hero.headlineAccent')}</span>
        </p>
        <p className="cf-auth__lead">{t('auth.hero.lead')}</p>
        <ul className="cf-auth__features">
          {FEATURES.map(({ icon: Icon, title: key, text }) => (
            <li key={key}>
              <span className="cf-auth__feature-icon" aria-hidden="true">
                <Icon size={24} />
              </span>
              <span>
                <strong>{t(key)}</strong>
                {text ? <span>{t(text)}</span> : null}
              </span>
            </li>
          ))}
        </ul>
        <p className="cf-auth__eyebrow">{t('auth.hero.eyebrow')}</p>
        <ul className="cf-auth__chips">
          {CHIPS.map(({ icon: Icon, title: key }) => (
            <li key={key}>
              <Icon size={18} />
              {t(key)}
            </li>
          ))}
        </ul>
        <p className="cf-auth__badge">
          <IconShieldCheck size={18} />
          {t('auth.hero.badge')}
        </p>
      </section>
      <div className="cf-auth__panel">
        <div className="cf-auth__card">
          <div className="cf-auth__brand">
            <BrandLogo size={72} />
          </div>
          <h1 className="cf-auth__title">{title}</h1>
          {subtitle ? <p className="cf-auth__subtitle">{subtitle}</p> : null}
          {children}
          {footer ? <div className="cf-auth__switch">{footer}</div> : null}
          <p className="cf-auth__legal">{t('auth.legal', { year: new Date().getFullYear() })}</p>
        </div>
      </div>
    </main>
  );
}
