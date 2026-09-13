import type { SuppressionReason } from '@/api/suppression';
import { Badge, type BadgeTone } from '@/design/components';
import { tEnum } from '@/i18n';

export function reasonTone(reason: SuppressionReason): BadgeTone {
  if (reason === 'complaint' || reason === 'hard_bounce') return 'danger';
  if (reason === 'unsubscribe') return 'warning';
  if (reason === 'manual') return 'info';
  return 'neutral';
}

export function ReasonBadge({ reason }: { reason: SuppressionReason }) {
  return <Badge tone={reasonTone(reason)}>{tEnum('suppression.reason', reason)}</Badge>;
}
