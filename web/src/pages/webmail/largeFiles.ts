import type { LargeFile, LargeFileListing, LargeFileState } from '@/api/largeFiles';
import type { BadgeTone } from '@/design/components';
import type { SelectOption } from '@/design/components';
import { t } from '@/i18n';
import { formatDate } from '@/lib/format';
import { formatBytes } from '@/lib/quota';
import { SIGNATURE_DELIMITER } from './richText';

// Reglas de la interfaz para los ficheros grandes por enlace. Los topes llegan en el listado de
// mail-files; aqui solo se aplican antes de subir para avisar sin esperar al servidor, que los aplica
// siempre.

const STATE_TONES: Record<LargeFileState, BadgeTone> = {
  uploading: 'info',
  active: 'success',
  expired: 'neutral',
  exhausted: 'warning',
  revoked: 'neutral',
  failed: 'danger',
};

export function largeFileStateTone(state: LargeFileState): BadgeTone {
  return STATE_TONES[state] ?? 'neutral';
}

/** Por que no se puede subir el fichero con el uso y los topes actuales; null si se puede. */
export function largeFileProblem(
  file: Pick<File, 'name' | 'size'>,
  listing: LargeFileListing,
): string | null {
  const { limits, usage } = listing;
  if (file.size > limits.max_file_bytes) {
    return t('webmail.largeFiles.compose.tooLarge', {
      name: file.name,
      size: formatBytes(limits.max_file_bytes),
    });
  }
  if (usage.mailbox_active >= limits.max_active_per_mailbox) {
    return t('webmail.largeFiles.compose.tooMany', { max: limits.max_active_per_mailbox });
  }
  const free = Math.max(0, limits.mailbox_quota_bytes - usage.mailbox_bytes);
  if (file.size > free) {
    return t('webmail.largeFiles.compose.noQuota', { name: file.name, free: formatBytes(free) });
  }
  return null;
}

// Plazos que se ofrecen, recortados al maximo del servicio; el por defecto y el maximo siempre estan.
const EXPIRY_STEPS = [1, 3, 7, 14, 30, 60, 90, 180, 365];

export function expiryOptions(defaultDays: number, maxDays: number): SelectOption[] {
  const days = new Set(EXPIRY_STEPS.filter((d) => d <= maxDays));
  days.add(defaultDays);
  days.add(maxDays);
  return [...days]
    .filter((d) => d >= 1 && d <= maxDays)
    .sort((a, b) => a - b)
    .map((d) => ({
      value: String(d),
      label:
        d === 1
          ? t('webmail.largeFiles.compose.expiryDay')
          : t('webmail.largeFiles.compose.expiryDays', { n: d }),
    }));
}

function linkDetails(file: LargeFile): string {
  return t('webmail.largeFiles.link.details', {
    size: formatBytes(file.size_bytes),
    date: formatDate(file.expires_at),
  });
}

/** Parrafo del cuerpo HTML con el enlace. El nombre lo eligio el usuario: va como texto, no como HTML. */
export function largeFileLinkHtml(file: LargeFile): string {
  const p = document.createElement('p');
  const a = document.createElement('a');
  a.setAttribute('href', file.url);
  a.textContent = file.name;
  p.append(`${t('webmail.largeFiles.link.prefix')} `, a, ` ${linkDetails(file)}`);
  return p.outerHTML;
}

export function largeFileLinkText(file: LargeFile): string {
  return `${t('webmail.largeFiles.link.prefix')} ${file.name} ${linkDetails(file)}\n${file.url}\n`;
}

/**
 * Pone el bloque del enlace donde el usuario escribe su mensaje: antes de la firma, que empieza por
 * el separador de RFC 3676 (richText.signatureText/signatureHtml). Sin firma, al principio si debajo
 * va el mensaje citado (responder, reenviar) y al final si no.
 */
export function insertLargeFileLink(
  body: string,
  block: string,
  format: 'html' | 'text',
  quoted: boolean,
): string {
  const marker =
    format === 'html' ? `<div>${SIGNATURE_DELIMITER}<br>` : `\n\n${SIGNATURE_DELIMITER}\n`;
  let at = body.indexOf(marker);
  if (at >= 0 && format === 'html') {
    // La firma HTML va precedida de una linea en blanco que tambien es suya.
    const blank = '<div><br></div>';
    if (body.slice(0, at).endsWith(blank)) at -= blank.length;
  }
  if (at < 0 && quoted) return `${block}${body}`;
  if (at < 0) {
    return format === 'text' && body !== '' && !body.endsWith('\n')
      ? `${body}\n${block}`
      : `${body}${block}`;
  }
  return `${body.slice(0, at)}${block}${body.slice(at)}`;
}
