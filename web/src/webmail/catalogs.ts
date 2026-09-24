import { sessionResource } from '@/api/resource';
import { webmailApi, type DavMeta } from '@/api/webmail';

/*
 * Catalogos del webmail ligados a la sesion del buzon: los topes del servicio (GET
 * /webmail/meta y /webmail/meta/dav), los remitentes del buzon (GET /webmail/identities) y su
 * firma (GET /webmail/signature). Una sola peticion por sesion para todas las pantallas. El
 * store los descarta al entrar, salir o caducar la sesion: otro buzon no hereda los del anterior.
 */
export const webmailMeta = sessionResource(() => webmailApi.meta());
export const davMeta = sessionResource(() => webmailApi.davMeta());
export const senderIdentities = sessionResource(() => webmailApi.identities());
/** Se descarta tambien al guardar la firma en los ajustes. */
export const mailboxSignature = sessionResource(() => webmailApi.signature());

export interface DavLimits {
  maxImportBytes: number | null;
  maxImportCards: number | null;
  maxEventWindowDays: number | null;
}

/**
 * Unico punto de lectura de los topes de contactos y calendario (los nombres son los de
 * mail-dav): si el servicio cambia donde o como los sirve, solo cambia esta funcion.
 */
export function davLimits(meta: DavMeta | null): DavLimits {
  const positive = (value: number | undefined) =>
    typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : null;
  return {
    maxImportBytes: positive(meta?.limits.max_import_bytes),
    maxImportCards: positive(meta?.limits.max_import_cards),
    maxEventWindowDays: positive(meta?.limits.max_event_window_days),
  };
}

export function resetWebmailCatalogs(): void {
  webmailMeta.reset();
  davMeta.reset();
  senderIdentities.reset();
  mailboxSignature.reset();
}
