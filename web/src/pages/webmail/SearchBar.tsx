import { useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { useResource } from '@/hooks/useResource';
import { Button, Checkbox, FormField, Input } from '@/design/components';
import { IconFilter, IconSearch, IconX } from '@/design/icons';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { webmailMeta } from '@/webmail/catalogs';
import {
  criteriaProblems,
  criteriaToView,
  EMPTY_CRITERIA,
  hasAdvanced,
  isSearching,
  readCriteria,
  type CriteriaField,
  type SearchCriteria,
} from './search';

export interface SearchBarProps {
  /** Carpeta en la que se busca: la abierta o, fuera del buzon, la de entrada. */
  folder: string | null;
  /** El atajo "/" pide el foco del buscador. */
  focusTick?: number;
  /** Fuera del buzon la URL no lleva criterios de busqueda (en la redaccion, ?to= es un destinatario). */
  readsUrl?: boolean;
}

/**
 * Buscador de la barra superior. Los criterios viajan en la URL del buzon; la vista y la
 * pestana abiertas se conservan y la paginacion vuelve a la primera pagina.
 */
export function SearchBar({ folder, focusTick = 0, readsUrl = true }: SearchBarProps) {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const criteria = useMemo(
    () => (readsUrl ? readCriteria(params) : EMPTY_CRITERIA),
    [readsUrl, params],
  );
  const criteriaKey = JSON.stringify(criteria);
  const [draft, setDraft] = useState<SearchCriteria>(criteria);
  const [advanced, setAdvanced] = useState(false);
  const [problems, setProblems] = useState<Partial<Record<CriteriaField, string>>>({});
  const inputRef = useRef<HTMLInputElement>(null);
  const formRef = useRef<HTMLFormElement>(null);
  // El tope de la busqueda es el del servicio (bytes UTF-8), no uno copiado aqui.
  const maxSearchBytes = useResource(webmailMeta).data?.limits.max_search_bytes ?? null;

  useEffect(() => {
    setDraft(criteria);
    setProblems({});
    // criteriaKey resume criteria: solo se sincroniza cuando cambia la busqueda de la URL.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [criteriaKey]);

  useEffect(() => {
    if (focusTick) inputRef.current?.focus();
  }, [focusTick]);

  useEffect(() => {
    if (!advanced) return;
    const onPointer = (e: PointerEvent) => {
      if (formRef.current && !formRef.current.contains(e.target as Node)) setAdvanced(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setAdvanced(false);
    };
    document.addEventListener('pointerdown', onPointer);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('pointerdown', onPointer);
      document.removeEventListener('keydown', onKey);
    };
  }, [advanced]);

  const edit = (patch: Partial<SearchCriteria>) => {
    setDraft((current) => ({ ...current, ...patch }));
    setProblems({});
  };

  const go = (next: SearchCriteria) => {
    const view = params.get('view');
    const tab = params.get('tab');
    navigate(
      folder
        ? paths.webmailView({
            folder,
            ...criteriaToView(next),
            view: view || undefined,
            tab: tab || undefined,
          })
        : paths.webmail,
    );
  };

  const submit = () => {
    const found = criteriaProblems(draft, maxSearchBytes);
    setProblems(found);
    if (Object.keys(found).length) {
      if (found.q === undefined) setAdvanced(true);
      return;
    }
    setAdvanced(false);
    go(draft);
  };

  const clear = () => {
    setDraft(EMPTY_CRITERIA);
    setProblems({});
    if (isSearching(criteria)) go(EMPTY_CRITERIA);
    inputRef.current?.focus();
  };

  const fieldError = (field: CriteriaField) => problems[field] ?? null;
  const filtered = hasAdvanced(criteria);

  return (
    <form
      ref={formRef}
      role="search"
      className="cf-wm-searchform"
      noValidate
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <div className="cf-wm-search">
        <Button
          type="submit"
          variant="ghost"
          iconOnly
          className="cf-wm-search__submit"
          icon={<IconSearch size={18} />}
        >
          {t('common.search')}
        </Button>
        <label htmlFor="wm-search" className="cf-visually-hidden">
          {t('webmail.list.search')}
        </label>
        <Input
          ref={inputRef}
          id="wm-search"
          type="search"
          value={draft.q}
          invalid={Boolean(problems.q)}
          aria-describedby={problems.q ? 'wm-search-error' : undefined}
          onChange={(e) => edit({ q: e.target.value })}
          placeholder={t('webmail.list.searchPlaceholder')}
        />
        {draft.q || isSearching(criteria) ? (
          <Button variant="ghost" iconOnly icon={<IconX size={18} />} onClick={clear}>
            {t('webmail.search.clear')}
          </Button>
        ) : null}
        <Button
          variant="ghost"
          iconOnly
          className={filtered ? 'cf-wm-search__filter--on' : undefined}
          icon={<IconFilter size={18} />}
          aria-expanded={advanced}
          aria-controls="wm-search-advanced"
          onClick={() => setAdvanced((open) => !open)}
        >
          {t('webmail.search.advanced')}
        </Button>
      </div>
      {problems.q ? (
        <div id="wm-search-error" className="cf-wm-search__error" role="alert">
          {problems.q}
        </div>
      ) : null}
      {advanced ? (
        <div id="wm-search-advanced" className="cf-wm-advanced">
          <FormField
            label={t('webmail.search.from')}
            htmlFor="wm-s-from"
            error={fieldError('from')}
          >
            <Input
              id="wm-s-from"
              value={draft.from}
              onChange={(e) => edit({ from: e.target.value })}
            />
          </FormField>
          <FormField label={t('webmail.search.to')} htmlFor="wm-s-to" error={fieldError('to')}>
            <Input id="wm-s-to" value={draft.to} onChange={(e) => edit({ to: e.target.value })} />
          </FormField>
          <FormField
            label={t('webmail.search.subject')}
            htmlFor="wm-s-subject"
            error={fieldError('subject')}
          >
            <Input
              id="wm-s-subject"
              value={draft.subject}
              onChange={(e) => edit({ subject: e.target.value })}
            />
          </FormField>
          <div className="cf-form__row">
            <FormField label={t('webmail.search.since')} htmlFor="wm-s-since">
              <Input
                id="wm-s-since"
                type="date"
                value={draft.since}
                onChange={(e) => edit({ since: e.target.value })}
              />
            </FormField>
            <FormField
              label={t('webmail.search.before')}
              htmlFor="wm-s-before"
              error={fieldError('before')}
            >
              <Input
                id="wm-s-before"
                type="date"
                value={draft.before}
                invalid={Boolean(problems.before)}
                onChange={(e) => edit({ before: e.target.value })}
              />
            </FormField>
          </div>
          <div className="cf-wm-advanced__marks">
            <Checkbox
              label={t('webmail.search.unread')}
              checked={draft.unread}
              onChange={(e) => edit({ unread: e.target.checked })}
            />
            <Checkbox
              label={t('webmail.search.flagged')}
              checked={draft.flagged}
              onChange={(e) => edit({ flagged: e.target.checked })}
            />
            <Checkbox
              label={t('webmail.search.attachments')}
              checked={draft.attachments}
              onChange={(e) => edit({ attachments: e.target.checked })}
            />
          </div>
          <div className="cf-form__actions">
            <Button
              size="sm"
              variant="ghost"
              onClick={() => {
                setDraft(EMPTY_CRITERIA);
                setProblems({});
              }}
            >
              {t('common.clear')}
            </Button>
            <Button size="sm" type="submit" variant="primary">
              {t('common.search')}
            </Button>
          </div>
        </div>
      ) : null}
    </form>
  );
}
