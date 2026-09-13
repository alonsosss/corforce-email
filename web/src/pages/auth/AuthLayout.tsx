import type { ReactNode } from 'react';
import { BrandMark } from '@/design/BrandMark';
import '@/layout/layout.css';

export interface AuthLayoutProps {
  title: string;
  subtitle?: string;
  children: ReactNode;
}

export function AuthLayout({ title, subtitle, children }: AuthLayoutProps) {
  return (
    <div className="cf-auth">
      <div className="cf-auth__card">
        <div className="cf-auth__brand">
          <BrandMark />
        </div>
        <h1 className="cf-auth__title">{title}</h1>
        {subtitle ? <p className="cf-auth__subtitle">{subtitle}</p> : null}
        {children}
      </div>
    </div>
  );
}
