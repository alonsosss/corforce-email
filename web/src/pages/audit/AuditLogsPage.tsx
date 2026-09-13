import { useState, type FormEvent } from 'react';
import {
  auditApi,
  SEVERITIES,
  type AuditLog,
  type AuditLogQuery,
  type Severity,
} from '@/api/audit';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import {
  Badge,
  Button,
  Card,
  DataTable,
  DescriptionList,
  Input,
  Modal,
  PageHeader,
  Select,
  type BadgeTone,
  type Column,
} from '@/design/components';
import { formatDateTime, localToRfc3339 } from '@/lib/format';
import { t, tEnum } from '@/i18n';

export function severityTone(severity: Severity): BadgeTone {
  if (severity === 'critical') return 'danger';
  if (severity === 'warning') return 'warning';
  return 'info';
}

interface Filters {
  module: string;
  resource: string;
  action: string;
  severity: string;
  user_id: string;
  ip_address: string;
  date_from: string;
  date_to: string;
}

const EMPTY: Filters = {
  module: '',
  resource: '',
  action: '',
  severity: '',
  user_id: '',
  ip_address: '',
  date_from: '',
  date_to: '',
};

function toQuery(f: Filters): Omit<AuditLogQuery, 'page' | 'per_page'> {
  return {
    module: f.module.trim() || undefined,
    resource: f.resource.trim() || undefined,
    action: f.action.trim() || undefined,
    severity: f.severity || undefined,
    user_id: f.user_id.trim() || undefined,
    ip_address: f.ip_address.trim() || undefined,
    date_from: localToRfc3339(f.date_from),
    date_to: localToRfc3339(f.date_to),
  };
}

export default function AuditLogsPage() {
  const pager = usePagination();
  const [draft, setDraft] = useState<Filters>(EMPTY);
  const [applied, setApplied] = useState<Filters>(EMPTY);
  const [selected, setSelected] = useState<AuditLog | null>(null);

  const logs = useQuery(
    () => auditApi.searchLogs({ page: pager.page, per_page: pager.perPage, ...toQuery(applied) }),
    [pager.page, pager.perPage, applied],
  );

  const apply = (e: FormEvent) => {
    e.preventDefault();
    setApplied(draft);
    pager.reset();
  };

  const field = (key: keyof Filters) => ({
    id: `audit-${key}`,
    value: draft[key],
    onChange: (e: { target: { value: string } }) =>
      setDraft((d) => ({ ...d, [key]: e.target.value })),
  });

  const columns: Column<AuditLog>[] = [
    {
      key: 'when',
      header: t('audit.logs.column.when'),
      render: (l) => formatDateTime(l.created_at),
    },
    {
      key: 'user',
      header: t('audit.logs.column.user'),
      render: (l) => <span className="cf-mono cf-text-sm">{l.user_id}</span>,
    },
    { key: 'module', header: t('audit.logs.column.module'), render: (l) => l.module },
    { key: 'resource', header: t('audit.logs.column.resource'), render: (l) => l.resource },
    { key: 'action', header: t('audit.logs.column.action'), render: (l) => l.action },
    {
      key: 'severity',
      header: t('audit.logs.column.severity'),
      render: (l) => (
        <Badge tone={severityTone(l.severity)}>{tEnum('audit.severity', l.severity)}</Badge>
      ),
    },
    {
      key: 'ip',
      header: t('audit.logs.column.ip'),
      render: (l) => <span className="cf-mono">{l.ip_address}</span>,
    },
  ];

  return (
    <div>
      <PageHeader title={t('audit.logs.title')} description={t('audit.logs.subtitle')} />
      <Card flush>
        <form className="cf-toolbar" onSubmit={apply}>
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="audit-module">
              {t('audit.logs.filter.module')}
            </label>
            <Input {...field('module')} />
          </div>
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="audit-resource">
              {t('audit.logs.filter.resource')}
            </label>
            <Input {...field('resource')} />
          </div>
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="audit-action">
              {t('audit.logs.filter.action')}
            </label>
            <Input {...field('action')} />
          </div>
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="audit-severity">
              {t('audit.logs.filter.severity')}
            </label>
            <Select
              {...field('severity')}
              placeholder={t('common.all')}
              options={SEVERITIES.map((s) => ({ value: s, label: tEnum('audit.severity', s) }))}
            />
          </div>
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="audit-user_id">
              {t('audit.logs.filter.userId')}
            </label>
            <Input {...field('user_id')} className="cf-mono" />
          </div>
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="audit-ip_address">
              {t('audit.logs.filter.ip')}
            </label>
            <Input {...field('ip_address')} className="cf-mono" />
          </div>
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="audit-date_from">
              {t('audit.logs.filter.from')}
            </label>
            <Input {...field('date_from')} type="datetime-local" />
          </div>
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="audit-date_to">
              {t('audit.logs.filter.to')}
            </label>
            <Input {...field('date_to')} type="datetime-local" />
          </div>
          <div className="cf-toolbar__actions">
            <Button
              onClick={() => {
                setDraft(EMPTY);
                setApplied(EMPTY);
                pager.reset();
              }}
            >
              {t('common.clear')}
            </Button>
            <Button type="submit" variant="primary">
              {t('common.apply')}
            </Button>
          </div>
        </form>
        <DataTable
          columns={columns}
          rows={logs.data?.items ?? []}
          rowKey={(l) => l.id}
          loading={logs.loading}
          error={logs.error}
          onRetry={logs.reload}
          empty={{ title: t('audit.logs.empty') }}
          onRowClick={setSelected}
          pagination={{
            page: logs.data?.page ?? pager.page,
            perPage: pager.perPage,
            total: logs.data?.total ?? 0,
            totalPages: logs.data?.totalPages ?? 0,
            onPageChange: pager.setPage,
          }}
        />
      </Card>
      {selected ? <AuditLogDetail log={selected} onClose={() => setSelected(null)} /> : null}
    </div>
  );
}

function AuditLogDetail({ log, onClose }: { log: AuditLog; onClose: () => void }) {
  return (
    <Modal open title={t('audit.logs.detailTitle')} onClose={onClose} size="lg">
      <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
        <DescriptionList
          items={[
            { label: t('audit.logs.column.when'), value: formatDateTime(log.created_at) },
            {
              label: t('audit.logs.column.user'),
              value: <span className="cf-mono">{log.user_id}</span>,
            },
            { label: t('audit.logs.column.module'), value: log.module },
            { label: t('audit.logs.column.resource'), value: log.resource },
            {
              label: t('audit.logs.detail.resourceId'),
              value: log.resource_id ? (
                <span className="cf-mono">{log.resource_id}</span>
              ) : (
                t('common.dash')
              ),
            },
            { label: t('audit.logs.column.action'), value: log.action },
            {
              label: t('audit.logs.column.severity'),
              value: (
                <Badge tone={severityTone(log.severity)}>
                  {tEnum('audit.severity', log.severity)}
                </Badge>
              ),
            },
            {
              label: t('audit.logs.column.ip'),
              value: <span className="cf-mono">{log.ip_address}</span>,
            },
            {
              label: t('audit.logs.detail.requestId'),
              value: log.request_id ? (
                <span className="cf-mono">{log.request_id}</span>
              ) : (
                t('common.dash')
              ),
            },
            { label: t('audit.logs.detail.userAgent'), value: log.user_agent ?? t('common.dash') },
            { label: t('common.id'), value: <span className="cf-mono">{log.id}</span> },
          ]}
        />
        {log.before ? (
          <div>
            <div className="cf-field__label">{t('audit.logs.detail.before')}</div>
            <pre className="cf-pre">{log.before}</pre>
          </div>
        ) : null}
        {log.after ? (
          <div>
            <div className="cf-field__label">{t('audit.logs.detail.after')}</div>
            <pre className="cf-pre">{log.after}</pre>
          </div>
        ) : null}
        {log.changes ? (
          <div>
            <div className="cf-field__label">{t('audit.logs.detail.changes')}</div>
            <pre className="cf-pre">{log.changes}</pre>
          </div>
        ) : null}
      </div>
    </Modal>
  );
}
