import { codeMessage } from '@/api/messages';
import { t } from '@/i18n';

// pause_reason y failure_reason llegan como "manual" o como "CODIGO: mensaje" del servicio
// que freno la campana (SENDING_RESTRICTED, TEMPLATE_MISSING_UNSUBSCRIBE...). El codigo se
// traduce con los mismos textos que los errores del API; el mensaje queda como detalle.

/** domain.PauseReasonManual. */
const MANUAL = 'manual';
const CODED = /^([A-Z][A-Z0-9_]*):\s*([\s\S]*)$/;

export interface ReasonView {
  text: string;
  detail: string | null;
}

export function describeReason(reason: string): ReasonView | null {
  const value = reason.trim();
  if (!value) return null;
  if (value === MANUAL) return { text: t('campaigns.reason.manual'), detail: null };
  const coded = CODED.exec(value);
  if (coded) {
    const [, code = '', message = ''] = coded;
    return { text: codeMessage(code) ?? code, detail: message.trim() || null };
  }
  return { text: t('campaigns.reason.other'), detail: value };
}
