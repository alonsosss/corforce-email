import { t } from '@/i18n';

/** Cifras de un codigo TOTP: las que genera pkg/totp y declara el URI de aprovisionamiento. */
export const TOTP_DIGITS = 6;

/** Con menos codigos de recuperacion que estos se aconseja generar otros. */
export const LOW_RECOVERY_CODES = 3;

/**
 * Codigo de recuperacion como lo guarda el servicio: sin espacios y en mayusculas. El guion
 * que separa los dos grupos se conserva si el usuario lo escribio; si no, se pone.
 */
export function normalizeRecoveryCode(raw: string): string {
  const compact = raw.replace(/[\s-]+/g, '').toUpperCase();
  if (compact.length % 2 !== 0 || compact.length === 0) return compact;
  const half = compact.length / 2;
  return `${compact.slice(0, half)}-${compact.slice(half)}`;
}

/** Secreto base32 en grupos de cuatro, para copiarlo a mano sin perderse. */
export function groupSecret(secret: string): string {
  return (secret.match(/.{1,4}/g) ?? []).join(' ');
}

/** Contenido del .txt de los codigos de recuperacion. */
export function recoveryCodesText(mailbox: string, codes: readonly string[]): string {
  return [t('webmail.security.recovery.fileHeader', { mailbox }), '', ...codes, ''].join('\n');
}
