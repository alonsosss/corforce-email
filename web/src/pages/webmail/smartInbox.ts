import { FLAGS, type MessageEnvelope } from '@/api/webmail';
import type { MessageKey } from '@/i18n';

/** Valor de ?view= de la vista por conversaciones; sin el, la vista por mensajes. */
export const THREADS_VIEW = 'threads';

/** Pestana que no filtra; las demas son las categorias que sirve el API (meta.inbox_categories). */
export const ALL_TAB = 'all';

// Textos de las categorias que la interfaz sabe nombrar. Una categoria nueva del servicio no se
// muestra hasta que tenga su texto: nunca aparece una pestana sin nombre.
const CATEGORY_LABELS: Partial<Record<string, MessageKey>> = {
  primary: 'webmail.tabs.primary',
  notifications: 'webmail.tabs.notifications',
  newsletters: 'webmail.tabs.newsletters',
};

export interface InboxTab {
  id: string;
  label: MessageKey;
}

/** Las pestanas de la bandeja: las categorias del servicio con texto, en su orden, y Todos. */
export function inboxTabs(categories: readonly string[]): InboxTab[] {
  const tabs: InboxTab[] = [];
  for (const id of categories) {
    const label = CATEGORY_LABELS[id];
    if (label) tabs.push({ id, label });
  }
  return tabs.length ? [...tabs, { id: ALL_TAB, label: 'webmail.tabs.all' }] : [];
}

/** La pestana activa: la de la URL si existe; si no, la primera (la principal). */
export function activeTab(raw: string | null, tabs: readonly InboxTab[]): string {
  if (raw && tabs.some((tab) => tab.id === raw)) return raw;
  return tabs[0]?.id ?? ALL_TAB;
}

/**
 * La categoria que se pide al API. Solo en la bandeja de entrada y sin busqueda: una busqueda
 * recorre todas las pestanas, como en los webmails del mercado.
 */
export function categoryFilter(
  tab: string,
  isInbox: boolean,
  searching: boolean,
): string | undefined {
  if (!isInbox || searching || tab === ALL_TAB) return undefined;
  return tab;
}

/** Los UIDs sobre los que actua una seleccion: una fila de conversacion arrastra todos los suyos. */
export function expandSelection(
  rows: readonly MessageEnvelope[],
  uids: readonly number[],
): number[] {
  const out = new Set<number>();
  for (const uid of uids) {
    const row = rows.find((r) => r.uid === uid);
    for (const member of row?.thread?.uids ?? [uid]) out.add(member);
  }
  return [...out];
}

/** Una fila esta sin leer si lo esta el mensaje o, en una conversacion, alguno de los suyos. */
export function rowUnread(row: MessageEnvelope, seen: boolean): boolean {
  return row.thread ? row.thread.unread > 0 : !seen;
}

/**
 * La fila con los flags nuevos de su mensaje. En una conversacion la fila es su ultimo mensaje: si
 * pasa a leido o a no leido, el contador de la conversacion lo sigue sin volver a pedir el listado.
 */
export function withRowFlags(row: MessageEnvelope, flags: string[]): MessageEnvelope {
  if (!row.thread) return { ...row, flags };
  const wasSeen = row.flags.includes(FLAGS.seen);
  const isSeen = flags.includes(FLAGS.seen);
  const delta = wasSeen === isSeen ? 0 : isSeen ? -1 : 1;
  return {
    ...row,
    flags,
    thread: { ...row.thread, unread: Math.max(0, row.thread.unread + delta) },
  };
}
