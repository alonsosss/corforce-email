import {
  contactsMeta,
  type ConsentStatus,
  type ContactStatus,
  type ContactStatusDetail,
} from '@/api/contacts';
import { Badge, type BadgeTone } from '@/design/components';
import { useResource } from '@/hooks/useResource';
import { hasMessage, t, tEnum } from '@/i18n';

type StatusDetails = readonly ContactStatusDetail[] | null | undefined;

export function statusDetail(
  status: ContactStatus,
  details: StatusDetails,
): ContactStatusDetail | undefined {
  return details?.find((d) => d.status === status);
}

/**
 * El tono sale de lo que levanta el estado segun el catalogo, no de una lista de estados:
 * un estado nuevo del servicio se pinta sin tocar la interfaz.
 */
export function contactStatusTone(status: ContactStatus, details: StatusDetails): BadgeTone {
  const detail = statusDetail(status, details);
  if (!detail) return 'neutral';
  switch (detail.lifted_by ?? '') {
    case '':
      return 'success';
    case 'reconsent':
      return 'warning';
    case 'operator_or_expiry':
      return 'info';
    case 'operator':
      return 'danger';
    default:
      return 'neutral';
  }
}

/** Explicacion de que levanta el estado; null en active o si no hay texto para ello. */
export function statusLiftHint(status: ContactStatus, details: StatusDetails): string | null {
  const lift = statusDetail(status, details)?.lifted_by;
  if (!lift) return null;
  const key = `contacts.statusLift.${lift}`;
  return hasMessage(key) ? t(key) : null;
}

export function consentTone(status: ConsentStatus): BadgeTone {
  if (status === 'granted') return 'success';
  if (status === 'pending') return 'info';
  if (status === 'revoked') return 'warning';
  return 'neutral';
}

function useStatusDetails(): StatusDetails {
  return useResource(contactsMeta).data?.status_details;
}

export function ContactStatusBadge({ status }: { status: ContactStatus }) {
  const details = useStatusDetails();
  return (
    <Badge tone={contactStatusTone(status, details)}>{tEnum('contacts.status', status)}</Badge>
  );
}

/** El estado con la explicacion de que lo levanta, para la ficha del contacto. */
export function ContactStatusSummary({ status }: { status: ContactStatus }) {
  const details = useStatusDetails();
  const hint = statusLiftHint(status, details);
  return (
    <div className="cf-cell-stack">
      <span>
        <Badge tone={contactStatusTone(status, details)}>{tEnum('contacts.status', status)}</Badge>
      </span>
      {hint ? <span className="cf-text-muted cf-text-sm">{hint}</span> : null}
    </div>
  );
}

export function ConsentBadge({ status }: { status: ConsentStatus }) {
  return <Badge tone={consentTone(status)}>{tEnum('contacts.consentStatus', status)}</Badge>;
}
