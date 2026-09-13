import { useEffect, useState } from 'react';
import { directoryMeta, mailDirectoryApi, type DirectoryDomain } from '@/api/mailDirectory';
import { useQuery } from '@/hooks/useQuery';
import { useResource } from '@/hooks/useResource';
import { Input, Select } from '@/design/components';
import { getLocale, t } from '@/i18n';

const SEARCH_DEBOUNCE_MS = 300;

export interface DirectoryDomainPickerProps {
  /** id del selector: el label del FormField apunta a el. */
  id: string;
  value: string;
  onChange: (domain: string) => void;
  /** Texto de la opcion vacia ("Todos" en un filtro, "Selecciona" en un formulario). */
  placeholder: string;
  invalid?: boolean;
  disabled?: boolean;
  optionLabel?: (domain: DirectoryDomain) => string;
}

/**
 * Selector de un dominio del directorio de la celda con busqueda en el servidor
 * (GET /mail-domains?search). Pide la pagina mas grande que admite el API y avisa si hay
 * mas coincidencias de las que caben. Sin permiso para leer el directorio (o si falla), el
 * dominio se escribe a mano: el servicio que recibe el valor lo vuelve a validar.
 */
export function DirectoryDomainPicker({
  id,
  value,
  onChange,
  placeholder,
  invalid,
  disabled,
  optionLabel,
}: DirectoryDomainPickerProps) {
  const meta = useResource(directoryMeta);
  const [input, setInput] = useState('');
  const [search, setSearch] = useState('');
  const pageSize = meta.data?.pagination.max_page_size ?? null;

  useEffect(() => {
    const handle = window.setTimeout(() => setSearch(input.trim()), SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(handle);
  }, [input]);

  const domains = useQuery(
    () =>
      pageSize === null
        ? Promise.resolve(null)
        : mailDirectoryApi.listDomains({
            page: 1,
            per_page: pageSize,
            search: search || undefined,
          }),
    [pageSize, search],
  );

  if (meta.error || domains.error) {
    return (
      <>
        <Input
          id={id}
          className="cf-mono"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          invalid={invalid}
          disabled={disabled}
          autoComplete="off"
          spellCheck={false}
          aria-describedby={`${id}-manual`}
        />
        <span className="cf-field__hint" id={`${id}-manual`}>
          {t('domainPicker.manual')}
        </span>
      </>
    );
  }

  const page = domains.data;
  const options = (page?.items ?? []).map((d) => ({
    value: d.domain,
    label: optionLabel ? optionLabel(d) : d.domain,
  }));
  if (value && !options.some((o) => o.value === value)) {
    options.unshift({ value, label: value });
  }
  const format = new Intl.NumberFormat(getLocale());
  const more = page !== null && page.total > page.items.length;

  return (
    <div className="cf-stack" style={{ gap: 'var(--cf-space-2)' }}>
      <Input
        id={`${id}-search`}
        type="search"
        aria-label={t('domainPicker.search')}
        placeholder={t('domainPicker.searchPlaceholder')}
        value={input}
        maxLength={meta.data?.search.max_length}
        onChange={(e) => setInput(e.target.value)}
        disabled={disabled}
        autoComplete="off"
        spellCheck={false}
      />
      <Select
        id={id}
        placeholder={domains.loading ? t('common.loading') : placeholder}
        options={options}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        invalid={invalid}
        disabled={disabled}
        aria-busy={domains.loading || undefined}
        aria-describedby={more ? `${id}-more` : undefined}
      />
      {more ? (
        <span className="cf-field__hint" id={`${id}-more`}>
          {t('domainPicker.more', {
            shown: format.format(page.items.length),
            total: format.format(page.total),
          })}
        </span>
      ) : null}
      {page !== null && page.total === 0 && search ? (
        <span className="cf-field__hint">{t('domainPicker.noMatches')}</span>
      ) : null}
    </div>
  );
}
