import { useMemo, useState, type FormEvent } from 'react';
import {
  LOG_DIRECTIONS,
  LOGS_MAX_LIMIT,
  LOGS_MAX_TEXT,
  observabilityApi,
  type LogDirection,
  type LogPage,
} from '@/api/observability';
import { isApiError, ERROR_CODES } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import { Alert, Button, Card, Input, PageHeader, Select, type SelectOption } from '@/design/components';
import { IconSearch } from '@/design/icons';
import { formatDateTime, formatTimestamp } from '@/lib/format';
import { t, type MessageKey } from '@/i18n';

/** Ventanas ofrecidas, en minutos; la mayor es el tope del API (24 h). */
const WINDOWS = [
  { id: '15m', minutes: 15, labelKey: 'logs.window.15m' },
  { id: '1h', minutes: 60, labelKey: 'logs.window.1h' },
  { id: '6h', minutes: 360, labelKey: 'logs.window.6h' },
  { id: '24h', minutes: 1440, labelKey: 'logs.window.24h' },
] as const satisfies readonly { id: string; minutes: number; labelKey: MessageKey }[];
type WindowId = (typeof WINDOWS)[number]['id'];

const LIMITS = [100, 200, LOGS_MAX_LIMIT] as const;

interface Draft {
  service: string;
  text: string;
  window: WindowId;
  direction: LogDirection;
  limit: number;
}

const EMPTY: Draft = { service: '', text: '', window: '1h', direction: 'backward', limit: 100 };

function validateText(text: string): string | null {
  if (/[\r\n]/.test(text)) return t('logs.textNoNewlines');
  if ([...text].length > LOGS_MAX_TEXT) return t('logs.textTooLong', { max: LOGS_MAX_TEXT });
  return null;
}

/**
 * Visor de registros de la plataforma: solo el superadmin (permiso de plataforma observability/logs).
 * La lista de servicios la sirve el API; el texto viaja tal cual y el servicio lo convierte en un
 * unico filtro literal. Las lineas se pintan como texto: nada se interpreta como HTML.
 */
export default function LogsPage() {
  const services = useQuery((signal) => observabilityApi.listLogServices(signal), []);
  const [draft, setDraft] = useState<Draft>(EMPTY);
  const [page, setPage] = useState<LogPage | null>(null);
  const [formError, setFormError] = useState<string | null>(null);

  const query = useAction(async (d: Draft) => {
    const minutes = WINDOWS.find((w) => w.id === d.window)?.minutes ?? 60;
    const since = new Date(Date.now() - minutes * 60_000).toISOString();
    setPage(
      await observabilityApi.queryLogs({
        service: d.service,
        q: d.text.trim() || undefined,
        since,
        limit: d.limit,
        direction: d.direction,
      }),
    );
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!draft.service) {
      setFormError(t('logs.serviceRequired'));
      return;
    }
    const textError = validateText(draft.text);
    setFormError(textError);
    if (textError) return;
    void query.run(draft);
  };

  const serviceOptions = useMemo<SelectOption[]>(
    () => (services.data ?? []).map((s) => ({ value: s, label: s })),
    [services.data],
  );
  const notConfigured =
    (isApiError(services.error) && services.error.code === ERROR_CODES.NOT_CONFIGURED) ||
    (isApiError(query.error) && query.error.code === ERROR_CODES.NOT_CONFIGURED);

  return (
    <div>
      <PageHeader title={t('logs.title')} description={t('logs.subtitle')} />
      {notConfigured ? <Alert tone="warning">{t('logs.notConfigured')}</Alert> : null}
      <Card flush>
        <form className="cf-toolbar" onSubmit={submit} aria-label={t('logs.title')}>
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="logs-service">
              {t('logs.filter.service')}
            </label>
            <Select
              id="logs-service"
              options={serviceOptions}
              placeholder={t('logs.filter.servicePlaceholder')}
              value={draft.service}
              disabled={services.loading}
              onChange={(e) => setDraft((d) => ({ ...d, service: e.target.value }))}
            />
          </div>
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="logs-text">
              {t('logs.filter.text')}
            </label>
            <Input
              id="logs-text"
              className="cf-mono"
              maxLength={LOGS_MAX_TEXT}
              value={draft.text}
              onChange={(e) => setDraft((d) => ({ ...d, text: e.target.value }))}
              invalid={Boolean(formError && formError !== t('logs.serviceRequired'))}
            />
          </div>
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="logs-window">
              {t('logs.filter.window')}
            </label>
            <Select
              id="logs-window"
              options={WINDOWS.map((w) => ({ value: w.id, label: t(w.labelKey) }))}
              value={draft.window}
              onChange={(e) => setDraft((d) => ({ ...d, window: e.target.value as WindowId }))}
            />
          </div>
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="logs-direction">
              {t('logs.filter.direction')}
            </label>
            <Select
              id="logs-direction"
              options={LOG_DIRECTIONS.map((d) => ({ value: d, label: t(`logs.direction.${d}`) }))}
              value={draft.direction}
              onChange={(e) =>
                setDraft((d) => ({ ...d, direction: e.target.value as LogDirection }))
              }
            />
          </div>
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="logs-limit">
              {t('logs.filter.limit')}
            </label>
            <Select
              id="logs-limit"
              options={LIMITS.map((n) => ({ value: String(n), label: String(n) }))}
              value={String(draft.limit)}
              onChange={(e) => setDraft((d) => ({ ...d, limit: Number(e.target.value) }))}
            />
          </div>
          <div className="cf-toolbar__actions">
            <Button type="submit" variant="primary" disabled={query.busy || notConfigured}>
              <IconSearch size={16} />
              {t('logs.search')}
            </Button>
          </div>
        </form>
        {formError ? (
          <div className="cf-stat">
            <Alert tone="danger">{formError}</Alert>
          </div>
        ) : null}
        {services.error && !notConfigured ? (
          <div className="cf-stat">
            <Alert tone="danger">{errorMessage(services.error)}</Alert>
          </div>
        ) : null}
        {query.error && !notConfigured ? (
          <div className="cf-stat">
            <Alert tone="danger">
              {isApiError(query.error) && query.error.code === 'LOGS_UNAVAILABLE'
                ? t('logs.unavailable')
                : errorMessage(query.error)}
            </Alert>
          </div>
        ) : null}
        <LogLines page={page} idle={!page && !query.busy && !query.error} />
      </Card>
    </div>
  );
}

function LogLines({ page, idle }: { page: LogPage | null; idle: boolean }) {
  if (idle || !page) {
    return <p className="cf-stat cf-text-sm cf-text-secondary">{idle ? t('logs.idle') : ''}</p>;
  }
  return (
    <div className="cf-stat">
      <p className="cf-text-sm cf-text-secondary">
        {t('logs.results', {
          service: page.service,
          since: formatDateTime(page.since),
          until: formatDateTime(page.until),
        })}
      </p>
      {page.truncated ? <Alert tone="info">{t('logs.truncated', { limit: page.limit })}</Alert> : null}
      {page.entries.length === 0 ? (
        <p className="cf-text-sm cf-text-secondary">{t('logs.empty')}</p>
      ) : (
        <ol className="cf-pre cf-pre--tall cf-log-lines" aria-label={t('logs.title')}>
          {page.entries.map((e, i) => (
            <li key={`${e.timestamp}-${i}`} className="cf-log-line">
              <span className="cf-text-muted">{formatTimestamp(e.timestamp)}</span>{' '}
              {e.labels.flujo === 'stderr' ? (
                <span className="cf-text-muted">[{t('logs.stream.stderr')}]</span>
              ) : null}{' '}
              <span className="cf-break">{e.line}</span>
            </li>
          ))}
        </ol>
      )}
    </div>
  );
}
