import type { ConsentStatus, ContactStatus } from '@/api/contacts';
import { Badge, type BadgeTone } from '@/design/components';
import { tEnum } from '@/i18n';

export function contactStatusTone(status: ContactStatus): BadgeTone {
  if (status === 'active') return 'success';
  if (status === 'unsubscribed') return 'warning';
  return 'danger';
}

export function consentTone(status: ConsentStatus): BadgeTone {
  if (status === 'granted') return 'success';
  if (status === 'pending') return 'info';
  if (status === 'revoked') return 'warning';
  return 'neutral';
}

export function ContactStatusBadge({ status }: { status: ContactStatus }) {
  return <Badge tone={contactStatusTone(status)}>{tEnum('contacts.status', status)}</Badge>;
}

export function ConsentBadge({ status }: { status: ConsentStatus }) {
  return <Badge tone={consentTone(status)}>{tEnum('contacts.consentStatus', status)}</Badge>;
}
