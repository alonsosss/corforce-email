import { useEffect, useRef } from 'react';
import type { MessageKey } from '@/i18n';

/*
 * Atajos de teclado del webmail. Una sola tecla sin modificadores, como los webmails del
 * mercado. Nunca se disparan mientras se escribe (campos, areas de texto, el editor con
 * formato) ni con un dialogo abierto: el dialogo es dueno del teclado.
 */

export interface ShortcutHelp {
  keys: string;
  label: MessageKey;
}

/** La lista que ensena la ayuda (?), en el orden en que se lee. */
export const SHORTCUTS: readonly ShortcutHelp[] = [
  { keys: 'c', label: 'webmail.shortcuts.compose' },
  { keys: 'r', label: 'webmail.shortcuts.reply' },
  { keys: 'a', label: 'webmail.shortcuts.replyAll' },
  { keys: 'f', label: 'webmail.shortcuts.forward' },
  { keys: 'e', label: 'webmail.shortcuts.archive' },
  { keys: '#', label: 'webmail.shortcuts.delete' },
  { keys: 'j', label: 'webmail.shortcuts.next' },
  { keys: 'k', label: 'webmail.shortcuts.previous' },
  { keys: '/', label: 'webmail.shortcuts.search' },
  { keys: '?', label: 'webmail.shortcuts.help' },
];

export function isTypingTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  if (target.isContentEditable || target.closest('[contenteditable="true"]')) return true;
  const tag = target.tagName;
  if (tag === 'TEXTAREA' || tag === 'SELECT') return true;
  if (tag !== 'INPUT') return false;
  const type = (target as HTMLInputElement).type;
  return !['checkbox', 'radio', 'button', 'submit', 'reset'].includes(type);
}

function dialogOpen(): boolean {
  return document.querySelector('[aria-modal="true"]') !== null;
}

export type ShortcutMap = Partial<Record<string, () => void>>;

/** Registra los atajos mientras el componente este montado; el mapa puede cambiar en cada render. */
export function useShortcuts(map: ShortcutMap, enabled = true): void {
  const current = useRef(map);
  current.current = map;

  useEffect(() => {
    if (!enabled) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.ctrlKey || e.metaKey || e.altKey) return;
      if (isTypingTarget(e.target) || dialogOpen()) return;
      const handler = current.current[e.key];
      if (!handler) return;
      e.preventDefault();
      handler();
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [enabled]);
}
