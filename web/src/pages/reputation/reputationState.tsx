import type { ReputationState } from '@/api/reputation';
import { Badge, type BadgeTone } from '@/design/components';
import { tEnum } from '@/i18n';

export function stateTone(state: ReputationState): BadgeTone {
  if (state === 'ok') return 'success';
  if (state === 'warning') return 'warning';
  return 'danger';
}

export function StateBadge({ state }: { state: ReputationState }) {
  return <Badge tone={stateTone(state)}>{tEnum('reputation.state', state)}</Badge>;
}

/**
 * Motivo del estado: un codigo de la evaluacion (within_thresholds, bounce_rate_block...)
 * o el texto que escribio el superadmin al suspender, que se muestra tal cual.
 */
export function reasonText(reason: string): string {
  return tEnum('reputation.reason', reason);
}
