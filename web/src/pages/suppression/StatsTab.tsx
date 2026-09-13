import { SUPPRESSION_REASONS, suppressionApi, type SuppressionReason } from '@/api/suppression';
import { useQuery } from '@/hooks/useQuery';
import { Card, DataTable, ErrorState, Skeleton, type Column } from '@/design/components';
import { getLocale, t } from '@/i18n';
import { ReasonBadge } from './suppressionReason';

interface ReasonRow {
  reason: SuppressionReason;
  count: number;
}

export function StatsTab() {
  const stats = useQuery(async () => (await suppressionApi.stats()).data, []);
  const format = new Intl.NumberFormat(getLocale());

  if (stats.error) {
    return (
      <Card title={t('suppression.stats.title')}>
        <ErrorState error={stats.error} onRetry={stats.reload} />
      </Card>
    );
  }
  if (!stats.data) {
    return (
      <Card title={t('suppression.stats.title')}>
        <Skeleton lines={5} />
      </Card>
    );
  }

  const byReason = stats.data.by_reason ?? {};
  const rows: ReasonRow[] = SUPPRESSION_REASONS.map((reason) => ({
    reason,
    count: byReason[reason] ?? 0,
  }));
  const columns: Column<ReasonRow>[] = [
    {
      key: 'reason',
      header: t('suppression.column.reason'),
      render: (r) => <ReasonBadge reason={r.reason} />,
    },
    {
      key: 'count',
      header: t('suppression.stats.count'),
      align: 'right',
      render: (r) => format.format(r.count),
    },
  ];

  return (
    <div className="cf-stack">
      <Card>
        <div className="cf-stat" style={{ padding: 0 }}>
          <div className="cf-stat__label">{t('suppression.stats.total')}</div>
          <div className="cf-stat__value">{format.format(stats.data.total)}</div>
        </div>
      </Card>
      <Card
        flush
        title={t('suppression.stats.title')}
        description={t('suppression.stats.description')}
      >
        <DataTable columns={columns} rows={rows} rowKey={(r) => r.reason} />
      </Card>
    </div>
  );
}
