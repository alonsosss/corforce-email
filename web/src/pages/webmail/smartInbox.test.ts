import { describe, expect, it } from 'vitest';
import type { MessageEnvelope } from '@/api/webmail';
import {
  ALL_TAB,
  activeTab,
  categoryFilter,
  expandSelection,
  inboxTabs,
  rowUnread,
  withRowFlags,
} from './smartInbox';

function row(uid: number, thread?: MessageEnvelope['thread']): MessageEnvelope {
  return {
    uid,
    from: [],
    to: [],
    cc: [],
    subject: '',
    date: null,
    flags: [],
    size: 0,
    has_attachments: false,
    thread,
  };
}

describe('bandeja inteligente', () => {
  it('las pestanas son las del servicio con texto, en su orden, y Todos al final', () => {
    const tabs = inboxTabs(['primary', 'desconocida', 'newsletters']);
    expect(tabs.map((tab) => tab.id)).toEqual(['primary', 'newsletters', ALL_TAB]);
    expect(inboxTabs([])).toEqual([]);
    expect(inboxTabs(['desconocida'])).toEqual([]);
  });

  it('la pestana activa sale de la URL si existe; si no, la primera', () => {
    const tabs = inboxTabs(['primary', 'notifications', 'newsletters']);
    expect(activeTab(null, tabs)).toBe('primary');
    expect(activeTab('newsletters', tabs)).toBe('newsletters');
    expect(activeTab('inventada', tabs)).toBe('primary');
    expect(activeTab('primary', [])).toBe(ALL_TAB);
  });

  it('solo filtra en la bandeja de entrada, sin busqueda y fuera de Todos', () => {
    expect(categoryFilter('primary', true, false)).toBe('primary');
    expect(categoryFilter('primary', false, false)).toBeUndefined();
    expect(categoryFilter('primary', true, true)).toBeUndefined();
    expect(categoryFilter(ALL_TAB, true, false)).toBeUndefined();
  });

  it('una fila de conversacion arrastra todos sus mensajes en una accion', () => {
    const rows = [row(9, { size: 3, unread: 1, uids: [9, 4, 2], participants: [] }), row(7)];
    expect(expandSelection(rows, [9, 7])).toEqual([9, 4, 2, 7]);
    expect(expandSelection(rows, [5])).toEqual([5]);
    expect(expandSelection([], [])).toEqual([]);
  });

  it('una conversacion esta sin leer si lo esta alguno de sus mensajes', () => {
    expect(rowUnread(row(1, { size: 2, unread: 1, uids: [1, 2], participants: [] }), true)).toBe(
      true,
    );
    expect(rowUnread(row(1, { size: 2, unread: 0, uids: [1, 2], participants: [] }), false)).toBe(
      false,
    );
    expect(rowUnread(row(1), false)).toBe(true);
    expect(rowUnread(row(1), true)).toBe(false);
  });

  it('abrir el ultimo mensaje de una conversacion descuenta su no leido', () => {
    const thread = { size: 2, unread: 2, uids: [5, 3], participants: [] };
    const seen = withRowFlags(row(5, thread), ['\\Seen']);
    expect(seen.flags).toEqual(['\\Seen']);
    expect(seen.thread?.unread).toBe(1);
    expect(withRowFlags(seen, []).thread?.unread).toBe(2);
    expect(withRowFlags(row(5, { ...thread, unread: 0 }), ['\\Seen']).thread?.unread).toBe(0);
    expect(withRowFlags(row(4), ['\\Flagged']).flags).toEqual(['\\Flagged']);
  });
});
