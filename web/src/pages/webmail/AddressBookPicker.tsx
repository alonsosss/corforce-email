import { useEffect, useState } from 'react';
import { webmailApi } from '@/api/webmail';
import { useQuery } from '@/hooks/useQuery';
import { Input, Skeleton } from '@/design/components';
import { t } from '@/i18n';
import { normalizeRecipient } from './compose';

const SEARCH_DELAY_MS = 250;

interface AddressBookPickerProps {
  /** Direcciones que ya son destinatarios: se marcan y no se pueden anadir dos veces. */
  chosen: string[];
  onPick: (address: string) => void;
}

/** Libreta de direcciones de la empresa: los companeros que el directorio de correo devuelve. */
export function AddressBookPicker({ chosen, onPick }: AddressBookPickerProps) {
  const [text, setText] = useState('');
  const [term, setTerm] = useState('');

  useEffect(() => {
    const timer = window.setTimeout(() => setTerm(text.trim()), SEARCH_DELAY_MS);
    return () => window.clearTimeout(timer);
  }, [text]);

  const results = useQuery((signal) => webmailApi.addressBook(term, signal), [term]);
  const taken = new Set(chosen.map((address) => address.toLowerCase()));

  return (
    <div className="cf-wm-addressbook">
      <Input
        type="search"
        value={text}
        onChange={(e) => setText(e.target.value)}
        placeholder={t('webmail.compose.addressBookSearch')}
        aria-label={t('webmail.compose.addressBookSearch')}
      />
      {results.error ? (
        <p role="alert" className="cf-field__hint">
          {t('webmail.compose.addressBookUnavailable')}
        </p>
      ) : results.loading && !results.data ? (
        <Skeleton lines={3} />
      ) : results.data && results.data.length === 0 ? (
        <p className="cf-field__hint">{t('webmail.compose.addressBookEmpty')}</p>
      ) : (
        <ul className="cf-wm-addressbook__list">
          {(results.data ?? []).map((entry) => {
            const address = normalizeRecipient(entry.address) ?? entry.address;
            const already = taken.has(address.toLowerCase());
            return (
              <li key={address}>
                <button
                  type="button"
                  className="cf-wm-addressbook__item"
                  disabled={already}
                  aria-label={t('webmail.compose.addressBookAdd', { address })}
                  onClick={() => onPick(address)}
                >
                  <span className="cf-wm-addressbook__name">{entry.display_name || address}</span>
                  {entry.display_name ? (
                    <span className="cf-wm-addressbook__address">{address}</span>
                  ) : null}
                  {already ? (
                    <span className="cf-wm-addressbook__state">
                      {t('webmail.compose.addressBookAdded')}
                    </span>
                  ) : null}
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
