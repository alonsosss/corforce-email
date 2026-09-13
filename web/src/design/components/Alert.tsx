import type { ReactNode } from 'react';
import { IconAlertCircle, IconAlertTriangle, IconCheckCircle, IconInfo } from '../icons';

export type AlertTone = 'info' | 'success' | 'warning' | 'danger';

export interface AlertProps {
  tone?: AlertTone;
  title?: string;
  children?: ReactNode;
}

function AlertIcon({ tone }: { tone: AlertTone }) {
  if (tone === 'success') return <IconCheckCircle size={18} />;
  if (tone === 'warning') return <IconAlertTriangle size={18} />;
  if (tone === 'danger') return <IconAlertCircle size={18} />;
  return <IconInfo size={18} />;
}

export function Alert({ tone = 'info', title, children }: AlertProps) {
  return (
    <div className={`cf-alert cf-alert--${tone}`} role={tone === 'danger' ? 'alert' : undefined}>
      <span className="cf-alert__icon">
        <AlertIcon tone={tone} />
      </span>
      <div className="cf-alert__body">
        {title ? <div className="cf-alert__title">{title}</div> : null}
        {children}
      </div>
    </div>
  );
}
