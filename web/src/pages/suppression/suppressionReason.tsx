import type { SuppressionReason } from '@/api/suppression';
import { Badge, type BadgeTone } from '@/design/components';
import { t, tEnum } from '@/i18n';

export function reasonTone(reason: SuppressionReason): BadgeTone {
  if (reason === 'complaint' || reason === 'hard_bounce') return 'danger';
  if (reason === 'unsubscribe') return 'warning';
  if (reason === 'manual') return 'info';
  return 'neutral';
}

export function ReasonBadge({ reason }: { reason: SuppressionReason }) {
  return <Badge tone={reasonTone(reason)}>{tEnum('suppression.reason', reason)}</Badge>;
}

/**
 * Causas vigentes de una direccion, de mas a menos grave como las da el servicio. La
 * primera es la principal y se marca cuando hay mas de una.
 */
export function ReasonList({
  reasons,
  primary,
}: {
  reasons: readonly SuppressionReason[] | null;
  primary: SuppressionReason;
}) {
  const ordered = reasons?.length ? reasons : [primary];
  return (
    <span className="cf-inline-list">
      {ordered.map((reason, index) => (
        <span key={reason} className="cf-inline" style={{ gap: 'var(--cf-space-1)' }}>
          <ReasonBadge reason={reason} />
          {index === 0 && ordered.length > 1 ? (
            <Badge tone="accent">{t('suppression.cause.primary')}</Badge>
          ) : null}
        </span>
      ))}
    </span>
  );
}
