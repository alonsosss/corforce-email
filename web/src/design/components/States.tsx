import type { ReactNode } from 'react';
import { IconAlertCircle, IconInbox } from '../icons';
import { Button } from './Button';
import { errorMessage } from '@/api/messages';
import { t } from '@/i18n';

export interface EmptyStateProps {
  title: string;
  description?: ReactNode;
  action?: ReactNode;
  icon?: ReactNode;
}

export function EmptyState({ title, description, action, icon }: EmptyStateProps) {
  return (
    <div className="cf-state">
      <div className="cf-state__icon">{icon ?? <IconInbox size={32} />}</div>
      <div className="cf-state__title">{title}</div>
      {description ? <div className="cf-state__description">{description}</div> : null}
      {action ? <div className="cf-state__action">{action}</div> : null}
    </div>
  );
}

export interface ErrorStateProps {
  error: unknown;
  title?: string;
  onRetry?: () => void;
}

export function ErrorState({ error, title, onRetry }: ErrorStateProps) {
  return (
    <div className="cf-state cf-state--error" role="alert">
      <div className="cf-state__icon">
        <IconAlertCircle size={32} />
      </div>
      <div className="cf-state__title">{title ?? t('error.loadFailed')}</div>
      <div className="cf-state__description">{errorMessage(error)}</div>
      {onRetry ? (
        <div className="cf-state__action">
          <Button onClick={onRetry}>{t('common.retry')}</Button>
        </div>
      ) : null}
    </div>
  );
}

export interface SkeletonProps {
  width?: string;
  height?: string;
  lines?: number;
}

export function Skeleton({ width = '100%', height = '14px', lines = 1 }: SkeletonProps) {
  if (lines <= 1) {
    return <span className="cf-skeleton" style={{ width, height }} aria-hidden="true" />;
  }
  return (
    <div className="cf-skeleton-lines" aria-hidden="true">
      {Array.from({ length: lines }, (_, i) => (
        <span
          key={i}
          className="cf-skeleton"
          style={{ width: i === lines - 1 ? '60%' : width, height }}
        />
      ))}
    </div>
  );
}

export function LoadingBlock({ label }: { label?: string }) {
  return (
    <div className="cf-state" role="status" aria-live="polite">
      <span className="cf-spinner cf-spinner--lg" aria-hidden="true" />
      <span className="cf-visually-hidden">{label ?? t('common.loading')}</span>
    </div>
  );
}
