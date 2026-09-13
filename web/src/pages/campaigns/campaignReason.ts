import { codeMessage } from '@/api/messages';
import { splitCodedReason } from '@/lib/codedReason';
import { t } from '@/i18n';

// pause_reason y failure_reason llegan como el motivo de la pausa manual (pause_reason_manual
// del catalogo) o como "CODIGO: mensaje" del servicio que freno la campana
// (SENDING_RESTRICTED, TEMPLATE_MISSING_UNSUBSCRIBE...). El codigo se traduce con los mismos
// textos que los errores del API; el mensaje queda como detalle.

export interface ReasonView {
  text: string;
  detail: string | null;
}

export function describeReason(reason: string, manual: string | null): ReasonView | null {
  const value = reason.trim();
  if (!value) return null;
  if (manual !== null && value === manual) {
    return { text: t('campaigns.reason.manual'), detail: null };
  }
  const coded = splitCodedReason(value);
  if (coded) {
    return { text: codeMessage(coded.code) ?? coded.code, detail: coded.detail || null };
  }
  return { text: t('campaigns.reason.other'), detail: value };
}
