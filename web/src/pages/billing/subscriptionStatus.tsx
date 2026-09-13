import type { PlanStatus, SubscriptionStatus } from '@/api/billing';
import { Badge, type BadgeTone } from '@/design/components';
import { tEnum } from '@/i18n';

export function subscriptionTone(status: SubscriptionStatus): BadgeTone {
  if (status === 'active') return 'success';
  if (status === 'trialing') return 'info';
  if (status === 'past_due') return 'warning';
  if (status === 'suspended') return 'danger';
  return 'neutral';
}

export function SubscriptionStatusBadge({ status }: { status: SubscriptionStatus }) {
  return (
    <Badge tone={subscriptionTone(status)}>{tEnum('billing.subscriptionStatus', status)}</Badge>
  );
}

export function PlanStatusBadge({ status }: { status: PlanStatus }) {
  return (
    <Badge tone={status === 'active' ? 'success' : 'neutral'}>
      {tEnum('billing.planStatus', status)}
    </Badge>
  );
}
