import type { DeliveryStatus } from '@/api/mailSecurity';
import type { MessageEvent, MessageStatus } from '@/api/transactional';
import type { BadgeTone } from '@/design/components';
import { localToRfc3339 } from '@/lib/format';

// Reglas de la pantalla Envios que no dependen de React.

export function messageStatusTone(status: MessageStatus): BadgeTone {
  switch (status) {
    case 'delivered':
      return 'success';
    case 'bounced':
    case 'complained':
    case 'rejected':
    case 'failed':
      return 'danger';
    case 'suppressed':
      return 'warning';
    default:
      return 'info';
  }
}

export function deliveryStatusTone(status: DeliveryStatus): BadgeTone {
  switch (status) {
    case 'sent':
      return 'success';
    case 'deferred':
      return 'warning';
    default:
      return 'danger';
  }
}

/**
 * Rango de dias locales (input type=date) a instantes RFC 3339: desde el inicio del primer dia y
 * hasta el inicio del dia siguiente al ultimo, para que "hasta el 26" incluya el 26 entero.
 */
export function dayRange(from: string, to: string): { date_from?: string; date_to?: string } {
  const out: { date_from?: string; date_to?: string } = {};
  if (from) out.date_from = localToRfc3339(`${from}T00:00`);
  if (to) {
    const end = new Date(`${to}T00:00`);
    if (!Number.isNaN(end.getTime())) {
      end.setDate(end.getDate() + 1);
      out.date_to = end.toISOString();
    }
  }
  return out;
}

/** El rango es valido si no hay limites o el primero no pasa del segundo. */
export function validRange(from: string, to: string): boolean {
  return !from || !to || from <= to;
}

/**
 * El motivo legible de un evento de SES: el codigo de diagnostico del servidor de destino si lo
 * hay, si no el motivo que resume SES. Vacio en los eventos sin motivo (entrega, apertura).
 */
export function eventReason(event: MessageEvent): string {
  const detail = event.detail ?? {};
  for (const key of ['diagnostic_code', 'reason', 'error', 'delay_type']) {
    const value = detail[key];
    if (typeof value === 'string' && value.trim()) return value.trim();
  }
  return '';
}

/** Los destinatarios de un mensaje, separados por coma. */
export function recipientsOf(list: readonly { email: string }[] | null | undefined): string {
  return (list ?? []).map((r) => r.email).join(', ');
}
