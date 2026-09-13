import { useState, type FormEvent } from 'react';
import { Link } from 'react-router-dom';
import {
  analyticsApi,
  analyticsMeta,
  type AnalyticsMeta,
  type CampaignReport,
  type Counters,
  type DomainReport,
  type Rates,
  type RangeQuery,
} from '@/api/analytics';
import { campaignsApi } from '@/api/campaigns';
import { PICKER_PAGE_SIZE } from '@/api/paging';
import type { SendClass } from '@/api/sendClass';
import { MODULES } from '@/access/modules';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import { useResource } from '@/hooks/useResource';
import {
  Alert,
  Button,
  Card,
  Checkbox,
  DataTable,
  ErrorState,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  type Column,
} from '@/design/components';
import { formatRate } from '@/lib/decimal';
import { formatDate } from '@/lib/format';
import { getLocale, t, tEnum } from '@/i18n';
import { paths } from '@/paths';
import { CampaignStatusBadge } from '@/pages/campaigns/campaignStatus';
import { MissingPermission } from '@/pages/shared/MissingPermission';
import { ResourceGate } from '@/pages/shared/ResourceGate';
import { availablePresets, parseDomainLimit, presetRange, rangeProblem } from './analyticsRange';
import { SeriesChart } from './SeriesChart';

// Nombres de los campos del DTO de analytics (domain.Counters y domain.Rates).
const COUNTER_KEYS: (keyof Counters)[] = [
  'sent',
  'delivered',
  'bounced_hard',
  'bounced_soft',
  'complained',
  'opened_unique',
  'clicked_unique',
  'unsubscribed',
  'failed',
];
const RATE_KEYS: (keyof Rates)[] = [
  'delivery',
  'bounce',
  'complaint',
  'open',
  'click',
  'unsubscribe',
];
const DEFAULT_CHART: (keyof Counters)[] = ['sent', 'delivered', 'opened_unique', 'clicked_unique'];

const RANGE_PROBLEM_KEY = {
  invalidDate: 'analytics.range.invalid',
  reversed: 'analytics.range.reversed',
  tooLong: 'analytics.range.tooLong',
} as const;

interface Filters {
  from: string;
  to: string;
  sendClass: SendClass | '';
}

const EMPTY: Filters = { from: '', to: '', sendClass: '' };

function toQuery(f: Filters): RangeQuery {
  return { from: f.from || undefined, to: f.to || undefined, class: f.sendClass || undefined };
}

export default function AnalyticsPage() {
  const { can } = useAccess();
  if (!can(...PERMISSIONS.analyticsReports.read)) {
    return <MissingPermission title={t('analytics.title')} description={t('analytics.subtitle')} />;
  }
  return <AnalyticsCatalog />;
}

/** Clases, zona, rango maximo y limites salen de GET /analytics/meta, no de constantes. */
function AnalyticsCatalog() {
  const meta = useResource(analyticsMeta);
  return (
    <div>
      <PageHeader title={t('analytics.title')} description={t('analytics.subtitle')} />
      <ResourceGate resource={meta}>{(data) => <AnalyticsView meta={data} />}</ResourceGate>
    </div>
  );
}

function AnalyticsView({ meta }: { meta: AnalyticsMeta }) {
  const { can, hasModule } = useAccess();
  const [draft, setDraft] = useState<Filters>(EMPTY);
  const [applied, setApplied] = useState<Filters>(EMPTY);
  const [problem, setProblem] = useState<string | null>(null);
  const [chartKeys, setChartKeys] = useState<(keyof Counters)[]>(DEFAULT_CHART);
  const [domainLimitInput, setDomainLimitInput] = useState('');
  const [domainLimit, setDomainLimit] = useState<number | undefined>(undefined);
  const [domainLimitError, setDomainLimitError] = useState<string | null>(null);
  const pager = usePagination(meta.pagination.default_per_page);
  const presets = availablePresets(meta.range.max_days, meta.range.default_days);
  const format = new Intl.NumberFormat(getLocale());

  const query = toQuery(applied);
  const overview = useQuery(() => analyticsApi.overview(query), [applied]);
  const series = useQuery(() => analyticsApi.timeseries(query), [applied]);
  const domains = useQuery(
    () => analyticsApi.domains({ ...query, limit: domainLimit }),
    [applied, domainLimit],
  );
  const campaigns = useQuery(
    () => analyticsApi.campaigns({ page: pager.page, per_page: pager.perPage }),
    [pager.page, pager.perPage],
  );
  const canReadCampaigns = can(...PERMISSIONS.campaigns.read);
  const campaignNames = useQuery(
    async () =>
      canReadCampaigns
        ? new Map(
            (await campaignsApi.list({ page: 1, per_page: PICKER_PAGE_SIZE })).items.map((c) => [
              c.id,
              c.name,
            ]),
          )
        : null,
    [canReadCampaigns],
  );

  const apply = (next: Filters) => {
    const issue = rangeProblem(next.from, next.to, meta.range.max_days);
    setProblem(
      issue ? t(RANGE_PROBLEM_KEY[issue], { max: format.format(meta.range.max_days) }) : null,
    );
    if (issue) return;
    setDraft(next);
    setApplied(next);
  };
  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    apply(draft);
  };
  const anchor = overview.data?.to;

  const domainColumns: Column<DomainReport>[] = [
    {
      key: 'domain',
      header: t('analytics.domains.domain'),
      render: (d) => <span className="cf-mono">{d.recipient_domain}</span>,
    },
    {
      key: 'sent',
      header: tEnum('analytics.counter', 'sent'),
      align: 'right',
      render: (d) => format.format(d.totals.sent),
    },
    {
      key: 'delivery',
      header: tEnum('analytics.rate', 'delivery'),
      align: 'right',
      render: (d) => formatRate(d.rates.delivery),
    },
    {
      key: 'bounce',
      header: tEnum('analytics.rate', 'bounce'),
      align: 'right',
      render: (d) => formatRate(d.rates.bounce),
    },
    {
      key: 'complaint',
      header: tEnum('analytics.rate', 'complaint'),
      align: 'right',
      render: (d) => formatRate(d.rates.complaint),
    },
  ];

  const campaignColumns: Column<CampaignReport>[] = [
    {
      key: 'campaign',
      header: t('analytics.campaigns.campaign'),
      render: (c) => {
        const name = campaignNames.data?.get(c.campaign_id);
        const label = name ?? <span className="cf-mono cf-text-sm">{c.campaign_id}</span>;
        return hasModule(MODULES.campaigns) ? (
          <Link to={paths.campaign(c.campaign_id)}>{label}</Link>
        ) : (
          label
        );
      },
    },
    {
      key: 'status',
      header: t('common.status'),
      render: (c) => (c.status ? <CampaignStatusBadge status={c.status} /> : t('common.dash')),
    },
    {
      key: 'days',
      header: t('analytics.campaigns.days'),
      render: (c) =>
        c.first_day && c.last_day
          ? t('analytics.campaigns.dayRange', {
              from: formatDate(`${c.first_day}T12:00:00Z`),
              to: formatDate(`${c.last_day}T12:00:00Z`),
            })
          : t('common.dash'),
    },
    {
      key: 'sent',
      header: tEnum('analytics.counter', 'sent'),
      align: 'right',
      render: (c) => format.format(c.totals.sent),
    },
    {
      key: 'delivery',
      header: tEnum('analytics.rate', 'delivery'),
      align: 'right',
      render: (c) => formatRate(c.rates.delivery),
    },
    {
      key: 'open',
      header: tEnum('analytics.rate', 'open'),
      align: 'right',
      render: (c) => formatRate(c.rates.open),
    },
    {
      key: 'click',
      header: tEnum('analytics.rate', 'click'),
      align: 'right',
      render: (c) => formatRate(c.rates.click),
    },
    {
      key: 'bounce',
      header: tEnum('analytics.rate', 'bounce'),
      align: 'right',
      render: (c) => formatRate(c.rates.bounce),
    },
  ];

  return (
    <div className="cf-stack">
      <Card>
        <form className="cf-form" onSubmit={onSubmit} noValidate>
          <span className="cf-field__hint">
            {t('analytics.range.hint', { tz: meta.timezone, n: meta.range.default_days })}
          </span>
          <div className="cf-form__row">
            <FormField label={t('analytics.range.from')} htmlFor="analytics-from">
              <Input
                id="analytics-from"
                type="date"
                value={draft.from}
                onChange={(e) => setDraft({ ...draft, from: e.target.value })}
              />
            </FormField>
            <FormField label={t('analytics.range.to')} htmlFor="analytics-to">
              <Input
                id="analytics-to"
                type="date"
                value={draft.to}
                onChange={(e) => setDraft({ ...draft, to: e.target.value })}
              />
            </FormField>
            <FormField label={t('analytics.class.label')} htmlFor="analytics-class">
              <Select
                id="analytics-class"
                placeholder={t('analytics.class.all')}
                options={meta.classes.map((c) => ({ value: c, label: tEnum('sendClass', c) }))}
                value={draft.sendClass}
                onChange={(e) =>
                  setDraft({ ...draft, sendClass: e.target.value as SendClass | '' })
                }
              />
            </FormField>
          </div>
          {problem ? (
            <div className="cf-form__error" role="alert">
              {problem}
            </div>
          ) : null}
          <div
            className="cf-form__actions"
            style={{ justifyContent: 'space-between', flexWrap: 'wrap' }}
          >
            <div className="cf-inline" role="group" aria-label={t('analytics.range.presets')}>
              {presets.map((days) => (
                <Button
                  key={days}
                  size="sm"
                  variant="ghost"
                  disabled={!anchor}
                  onClick={() => anchor && apply({ ...draft, ...presetRange(days, anchor) })}
                >
                  {t('analytics.range.lastDays', { n: days })}
                </Button>
              ))}
            </div>
            <Button type="submit" variant="primary">
              {t('common.apply')}
            </Button>
          </div>
        </form>
      </Card>

      {overview.data ? (
        <Alert tone="info">
          {t('analytics.timezoneNote', {
            tz: overview.data.timezone,
            from: formatDate(`${overview.data.from}T12:00:00Z`),
            to: formatDate(`${overview.data.to}T12:00:00Z`),
          })}
        </Alert>
      ) : null}

      <Card title={t('analytics.overview.title')}>
        {overview.error ? (
          <ErrorState error={overview.error} onRetry={overview.reload} />
        ) : !overview.data ? (
          <Skeleton lines={4} />
        ) : (
          <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
            <div className="cf-kpis">
              {RATE_KEYS.map((key) => (
                <div key={key} className="cf-kpi">
                  <span className="cf-kpi__label">{tEnum('analytics.rate', key)}</span>
                  <span className="cf-kpi__value">
                    {formatRate(overview.data?.rates[key] ?? '')}
                  </span>
                </div>
              ))}
            </div>
            <div className="cf-kpis">
              {COUNTER_KEYS.map((key) => (
                <div key={key} className="cf-kpi">
                  <span className="cf-kpi__label">{tEnum('analytics.counter', key)}</span>
                  <span className="cf-kpi__value">
                    {format.format(overview.data?.totals[key] ?? 0)}
                  </span>
                </div>
              ))}
            </div>
          </div>
        )}
      </Card>

      <Card title={t('analytics.chart.title')} description={t('analytics.chart.description')}>
        <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
          <div className="cf-inline-list" role="group" aria-label={t('analytics.chart.series')}>
            {COUNTER_KEYS.map((key) => (
              <Checkbox
                key={key}
                label={tEnum('analytics.counter', key)}
                checked={chartKeys.includes(key)}
                onChange={(e) =>
                  setChartKeys((keys) =>
                    e.target.checked
                      ? COUNTER_KEYS.filter((k) => k === key || keys.includes(k))
                      : keys.filter((k) => k !== key),
                  )
                }
              />
            ))}
          </div>
          {series.error ? (
            <ErrorState error={series.error} onRetry={series.reload} />
          ) : !series.data ? (
            <Skeleton height="220px" />
          ) : series.data.points.length === 0 || chartKeys.length === 0 ? (
            <span className="cf-text-muted cf-text-sm">{t('analytics.chart.empty')}</span>
          ) : (
            <SeriesChart
              title={t('analytics.chart.title')}
              days={series.data.points.map((p) => p.day)}
              series={chartKeys.map((key) => ({
                key,
                label: tEnum('analytics.counter', key),
                values: series.data?.points.map((p) => p[key]) ?? [],
              }))}
            />
          )}
        </div>
      </Card>

      <Card
        flush
        title={t('analytics.domains.title')}
        description={t('analytics.domains.description')}
      >
        <form
          className="cf-toolbar"
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            const parsed = parseDomainLimit(domainLimitInput, meta.domains.max_limit);
            setDomainLimitError(
              parsed
                ? null
                : t('analytics.domains.limitInvalid', {
                    max: format.format(meta.domains.max_limit),
                  }),
            );
            if (parsed) setDomainLimit(parsed.limit);
          }}
        >
          <FormField
            label={t('analytics.domains.limit')}
            htmlFor="analytics-domain-limit"
            error={domainLimitError}
            hint={t('analytics.domains.limitHint', {
              n: format.format(meta.domains.default_limit),
              max: format.format(meta.domains.max_limit),
            })}
          >
            <Input
              id="analytics-domain-limit"
              inputMode="numeric"
              placeholder={String(meta.domains.default_limit)}
              value={domainLimitInput}
              onChange={(e) => setDomainLimitInput(e.target.value)}
              invalid={Boolean(domainLimitError)}
            />
          </FormField>
          <div className="cf-toolbar__actions">
            <Button type="submit">{t('common.apply')}</Button>
          </div>
        </form>
        <DataTable
          columns={domainColumns}
          rows={domains.data?.domains ?? []}
          rowKey={(d) => d.recipient_domain}
          loading={domains.loading}
          error={domains.error}
          onRetry={domains.reload}
          empty={{ title: t('analytics.domains.empty') }}
        />
      </Card>

      <Card
        flush
        title={t('analytics.campaigns.title')}
        description={t('analytics.campaigns.description')}
      >
        <DataTable
          columns={campaignColumns}
          rows={campaigns.data?.items ?? []}
          rowKey={(c) => c.campaign_id}
          loading={campaigns.loading}
          error={campaigns.error}
          onRetry={campaigns.reload}
          empty={{ title: t('analytics.campaigns.empty') }}
          pagination={{
            page: campaigns.data?.page ?? pager.page,
            perPage: pager.perPage,
            total: campaigns.data?.total ?? 0,
            totalPages: campaigns.data?.totalPages ?? 0,
            onPageChange: pager.setPage,
          }}
        />
      </Card>
    </div>
  );
}
