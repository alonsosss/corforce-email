import { sessionResource } from '@/api/resource';
import { webmailApi } from '@/api/webmail';

/*
 * Catalogos del webmail ligados a la sesion del buzon: los topes del servicio (GET
 * /webmail/meta) y los remitentes del buzon (GET /webmail/identities). Una sola peticion
 * por sesion para todas las pantallas. El store los descarta al entrar, salir o caducar la
 * sesion: otro buzon no hereda los remitentes del anterior.
 */
export const webmailMeta = sessionResource(() => webmailApi.meta());
export const senderIdentities = sessionResource(() => webmailApi.identities());

export function resetWebmailCatalogs(): void {
  webmailMeta.reset();
  senderIdentities.reset();
}
