import type { DomainStatus, VerifyOutcome } from '@/api/domains';
import type { AlertTone, BadgeTone } from '@/design/components';

export function domainStatusTone(status: DomainStatus): BadgeTone {
  if (status === 'verified') return 'success';
  if (status === 'failed') return 'danger';
  if (status === 'pending') return 'warning';
  return 'neutral';
}

export function verifyOutcomeTone(outcome: VerifyOutcome): AlertTone {
  if (outcome === 'verified') return 'success';
  if (outcome === 'failed') return 'danger';
  return 'warning';
}
