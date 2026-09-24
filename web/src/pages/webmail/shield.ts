import type { ShieldLevel, ShieldReason } from '@/api/webmail';
import { hasMessage, t } from '@/i18n';

/** Explicacion de un motivo del escudo con sus datos (el dominio parecido, el companero). */
export function reasonText(reason: ShieldReason): string {
  const key = `webmail.shield.reason.${reason.code}`;
  return hasMessage(key) ? t(key, reason.params) : t('webmail.shield.reason.unknown');
}

/** Solo los avisos de riesgo se explican en la banda; "info" es la marca de remitente externo. */
export function isWarning(level: ShieldLevel): level is 'caution' | 'danger' {
  return level === 'caution' || level === 'danger';
}

/** Los motivos que se explican: todos menos el de remitente externo, que ya dice la marca. */
export function warningReasons(reasons: readonly ShieldReason[]): ShieldReason[] {
  return reasons.filter((r) => isWarning(r.level));
}

/** Enlace que se puede ofrecer para abrir una pagina de baja: solo https. */
export function safeUnsubscribeURL(url: string | undefined): string | null {
  if (!url) return null;
  try {
    return new URL(url).protocol === 'https:' ? url : null;
  } catch {
    return null;
  }
}
