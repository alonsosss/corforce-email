import { useEffect, useState } from 'react';
import { webmailApi, type AddressBookEntry, type Contact } from '@/api/webmail';
import type { ChipSuggestion } from '@/design/components';
import { t } from '@/i18n';
import { normalizeRecipient } from './compose';

const SEARCH_DELAY_MS = 250;
const MIN_QUERY_CHARS = 2;
const MAX_SUGGESTIONS = 8;

/**
 * Propuestas de destinatario: primero la agenda personal (una ficha puede tener varias
 * direcciones) y despues la libreta de la empresa, sin repetir direcciones. Si una de las
 * dos fuentes falla se usan las propuestas de la otra: autocompletar nunca bloquea escribir.
 */
export function mergeSuggestions(
  contacts: readonly Contact[],
  colleagues: readonly AddressBookEntry[],
): ChipSuggestion[] {
  const seen = new Set<string>();
  const out: ChipSuggestion[] = [];
  const add = (raw: string, name: string, source: string) => {
    const email = normalizeRecipient(raw);
    if (!email || seen.has(email.toLowerCase())) return;
    seen.add(email.toLowerCase());
    out.push({
      value: email,
      label: name.trim() || email,
      detail: name.trim() ? `${email} - ${source}` : source,
    });
  };
  for (const contact of contacts) {
    for (const email of contact.emails) {
      add(email.value, contact.name, t('webmail.suggest.personal'));
    }
  }
  for (const entry of colleagues)
    add(entry.address, entry.display_name, t('webmail.suggest.company'));
  return out.slice(0, MAX_SUGGESTIONS);
}

export function useRecipientSuggestions(query: string): ChipSuggestion[] {
  const [suggestions, setSuggestions] = useState<ChipSuggestion[]>([]);

  useEffect(() => {
    const term = query.trim();
    if (term.length < MIN_QUERY_CHARS) {
      setSuggestions([]);
      return;
    }
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      void Promise.allSettled([
        webmailApi.contacts({ q: term }, controller.signal),
        webmailApi.addressBook(term, controller.signal),
      ]).then(([personal, company]) => {
        if (controller.signal.aborted) return;
        setSuggestions(
          mergeSuggestions(
            personal.status === 'fulfilled' ? personal.value.items : [],
            company.status === 'fulfilled' ? company.value : [],
          ),
        );
      });
    }, SEARCH_DELAY_MS);
    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [query]);

  return suggestions;
}
