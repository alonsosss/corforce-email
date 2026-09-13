import { useState } from 'react';
import { accessApi, type DenialRecord, type DenialSummaryRow } from '@/api/access';
import { useQuery } from '@/hooks/useQuery';
import { Badge, Card, DataTable, PageHeader, Select, type Column } from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { t } from '@/i18n';

// Ventanas que admite GET /access/denials (?days= entre 1 y 90).
const WINDOWS = [7, 30, 90] as const;
const RECENT_LIMIT = 50;

export default function DenialsPage() {
  const [days, setDays] = useState<number>(WINDOWS[0]);
  const metrics = useQuery(
    async () => (await accessApi.denials({ days, limit: RECENT_LIMIT })).data,
    [days],
  );

  const summaryColumns = (label: string): Column<DenialSummaryRow>[] => [
    { key: 'key', header: label, render: (r) => <span className="cf-mono">{r.key}</span> },
    { key: 'count', header: t('denials.column.count'), align: 'right', render: (r) => r.count },
  ];

  const recentColumns: Column<DenialRecord>[] = [
    { key: 'when', header: t('denials.column.when'), render: (r) => formatDateTime(r.created_at) },
    {
      key: 'user',
      header: t('denials.column.user'),
      render: (r) => <span className="cf-mono">{r.user_id}</span>,
    },
    { key: 'module', header: t('denials.column.module'), render: (r) => r.module },
    {
      key: 'action',
      header: t('denials.column.action'),
      render: (r) => r.action || t('common.dash'),
    },
    {
      key: 'method',
      header: t('denials.column.method'),
      render: (r) => <span className="cf-mono">{r.method}</span>,
    },
    {
      key: 'path',
      header: t('denials.column.path'),
      render: (r) => <span className="cf-mono">{r.path}</span>,
    },
    {
      key: 'enforced',
      header: t('denials.column.enforced'),
      render: (r) =>
        r.enforced ? (
          <Badge tone="danger">{t('common.yes')}</Badge>
        ) : (
          <Badge>{t('denials.audited')}</Badge>
        ),
    },
  ];

  const m = metrics.data;

  return (
    <div className="cf-stack">
      <PageHeader
        title={t('denials.title')}
        description={t('denials.subtitle')}
        actions={
          <div className="cf-field" style={{ minWidth: 160 }}>
            <label className="cf-field__label" htmlFor="denials-days">
              {t('denials.days')}
            </label>
            <Select
              id="denials-days"
              options={WINDOWS.map((w) => ({ value: String(w), label: String(w) }))}
              value={String(days)}
              onChange={(e) => setDays(Number(e.target.value))}
            />
          </div>
        }
      />
      <Card>
        <div className="cf-stat" style={{ padding: 0 }}>
          <div className="cf-stat__label">{t('denials.total')}</div>
          <div className="cf-stat__value">{m ? m.total : t('common.dash')}</div>
        </div>
      </Card>
      <div
        className="cf-grid-cards"
        style={{ gridTemplateColumns: 'repeat(auto-fit, minmax(320px, 1fr))' }}
      >
        <Card title={t('denials.byModule')} flush>
          <DataTable
            columns={summaryColumns(t('denials.column.module'))}
            rows={m?.by_module ?? []}
            rowKey={(r) => r.key}
            loading={metrics.loading}
            error={metrics.error}
            onRetry={metrics.reload}
            empty={{ title: t('denials.empty') }}
            skeletonRows={3}
          />
        </Card>
        <Card title={t('denials.byUser')} flush>
          <DataTable
            columns={summaryColumns(t('denials.column.user'))}
            rows={m?.by_user ?? []}
            rowKey={(r) => r.key}
            loading={metrics.loading}
            error={metrics.error}
            onRetry={metrics.reload}
            empty={{ title: t('denials.empty') }}
            skeletonRows={3}
          />
        </Card>
      </div>
      <Card title={t('denials.recent')} flush>
        <DataTable
          columns={recentColumns}
          rows={m?.recent ?? []}
          rowKey={(r) => `${r.created_at}-${r.user_id}-${r.path}`}
          loading={metrics.loading}
          error={metrics.error}
          onRetry={metrics.reload}
          empty={{ title: t('denials.empty') }}
        />
      </Card>
    </div>
  );
}
