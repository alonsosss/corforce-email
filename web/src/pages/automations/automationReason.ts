import { codeMessage } from '@/api/messages';
import { splitCodedReason } from '@/lib/codedReason';
import { hasMessage, t } from '@/i18n';

// Motivos de automations: la pausa de un flujo (el manual del catalogo, el texto de quien
// pauso o "CODIGO: detalle" del ejecutor), el error de una ejecucion (error_code y
// last_error) y el de un intento del doble opt-in (motivo propio o "CODIGO: detalle"). El
// codigo se traduce; un codigo sin texto se muestra tal cual, sin inventar uno.

export interface ReasonView {
  text: string;
  detail: string | null;
}

function codeText(prefix: string, code: string): string {
  const key = `${prefix}.${code}`;
  if (hasMessage(key)) return t(key);
  return codeMessage(code) ?? code;
}

export function describePause(reason: string, manual: string | null): ReasonView | null {
  const value = reason.trim();
  if (!value) return null;
  if (manual !== null && value === manual) {
    return { text: t('automations.pause.manual'), detail: null };
  }
  const coded = splitCodedReason(value);
  if (coded) {
    return { text: codeText('automations.runError', coded.code), detail: coded.detail || null };
  }
  return { text: t('automations.pause.byPerson'), detail: value };
}

export function describeRunError(code: string, lastError: string): ReasonView | null {
  const trimmedCode = code.trim();
  const message = lastError.trim();
  if (!trimmedCode && !message) return null;
  if (!trimmedCode) return { text: t('automations.reason.other'), detail: message };
  return { text: codeText('automations.runError', trimmedCode), detail: message || null };
}

export function describeDoiReason(reason: string): ReasonView | null {
  const value = reason.trim();
  if (!value) return null;
  const own = `automations.doiReason.${value}`;
  if (hasMessage(own)) return { text: t(own), detail: null };
  const coded = splitCodedReason(value);
  if (coded) {
    return { text: codeText('automations.doiReason', coded.code), detail: coded.detail || null };
  }
  return { text: t('automations.reason.other'), detail: value };
}
