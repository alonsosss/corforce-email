import { UNLIMITED, type BillingResource, type UsageReport } from '@/api/billing';
import { Badge, DataTable, Meter, type Column } from '@/design/components';
import { barRatio, formatDecimalText, formatPercentText } from '@/lib/decimal';
import { formatDate } from '@/lib/format';
import { formatBytes } from '@/lib/quota';
import { getLocale, t, tEnum } from '@/i18n';

export function formatQuantity(resource: BillingResource, n: number): string {
  return resource === 'storage_bytes'
    ? formatBytes(n)
    : new Intl.NumberFormat(getLocale()).format(n);
}

export function formatIncluded(resource: BillingResource, included: number): string {
  return included === UNLIMITED ? t('billing.unlimited') : formatQuantity(resource, included);
}

/** Importe del API ("49.00") con su moneda, sin convertirlo a numero. */
export function formatMoney(amount: string, currency: string): string {
  return `${formatDecimalText(amount)} ${currency}`;
}

/** Los periodos son dias de calendario UTC (AAAA-MM-DD). */
export function formatPeriodDay(day: string): string {
  return formatDate(`${day}T12:00:00Z`);
}

type Line = UsageReport['resources'][number];

export function UsageTable({ report }: { report: UsageReport }) {
  const columns: Column<Line>[] = [
    {
      key: 'resource',
      header: t('billing.column.resource'),
      render: (l) => (
        <div className="cf-cell-stack">
          <strong>{tEnum('billing.resource', l.resource)}</strong>
          <span className="cf-text-muted cf-text-sm">{tEnum('billing.kind', l.kind)}</span>
        </div>
      ),
    },
    {
      key: 'usage',
      header: t('billing.column.usage'),
      render: (l) => (
        <div className="cf-stack" style={{ gap: 'var(--cf-space-1)', minWidth: 160 }}>
          <span className="cf-text-sm">
            {t('billing.usedOf', {
              used: formatQuantity(l.resource, l.used),
              included: formatIncluded(l.resource, l.included),
            })}
          </span>
          {l.percent !== null ? (
            <Meter
              ratio={barRatio(l.percent)}
              label={t('billing.meterLabel', { resource: tEnum('billing.resource', l.resource) })}
            />
          ) : null}
        </div>
      ),
    },
    {
      key: 'percent',
      header: t('billing.column.percent'),
      align: 'right',
      render: (l) => (l.percent !== null ? formatPercentText(l.percent) : t('common.dash')),
    },
    {
      key: 'limit',
      header: t('billing.column.limitKind'),
      render: (l) =>
        l.included === UNLIMITED ? (
          <Badge>{t('billing.unlimited')}</Badge>
        ) : l.hard_limit ? (
          <Badge tone="warning">{t('billing.hardLimit')}</Badge>
        ) : (
          <Badge tone="info">{t('billing.softLimit')}</Badge>
        ),
    },
    {
      key: 'overage',
      header: t('billing.column.overage'),
      align: 'right',
      render: (l) =>
        l.overage > 0 ? (
          <Badge tone="danger">{formatQuantity(l.resource, l.overage)}</Badge>
        ) : (
          t('common.dash')
        ),
    },
  ];
  return (
    <DataTable
      columns={columns}
      rows={report.resources}
      rowKey={(l) => l.resource}
      empty={{ title: t('billing.usage.empty') }}
    />
  );
}
