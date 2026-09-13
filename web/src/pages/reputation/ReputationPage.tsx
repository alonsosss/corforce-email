import { useState } from 'react';
import {
  reputationApi,
  reputationMeta,
  type ClassStatus,
  type RateUsage,
  type StateChange,
} from '@/api/reputation';
import type { SendClass } from '@/api/sendClass';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import { useResource } from '@/hooks/useResource';
import {
  Alert,
  Badge,
  Card,
  DataTable,
  DescriptionList,
  ErrorState,
  Meter,
  PageHeader,
  Select,
  Skeleton,
  type Column,
} from '@/design/components';
import { formatRate } from '@/lib/decimal';
import { formatDate, formatDateTime } from '@/lib/format';
import { getLocale, t, tEnum } from '@/i18n';
import { MissingPermission } from '@/pages/shared/MissingPermission';
import { reasonText, StateBadge } from './reputationState';

export default function ReputationPage() {
  const { can } = useAccess();
  const canStatus = can(...PERMISSIONS.reputationStatus.read);
  const canHistory = can(...PERMISSIONS.reputationHistory.read);
  const status = useQuery(
    async () => (canStatus ? (await reputationApi.status()).data : null),
    [canStatus],
  );
  const meta = useResource(reputationMeta);
  if (!canStatus && !canHistory) {
    return (
      <MissingPermission title={t('reputation.title')} description={t('reputation.subtitle')} />
    );
  }
  const classes = status.data?.classes.map((c) => c.class) ?? meta.data?.classes ?? [];
  return (
    <div>
      <PageHeader title={t('reputation.title')} description={t('reputation.subtitle')} />
      <div className="cf-stack">
        {canStatus ? (
          status.error ? (
            <Card>
              <ErrorState error={status.error} onRetry={status.reload} />
            </Card>
          ) : !status.data ? (
            <Card>
              <Skeleton lines={6} />
            </Card>
          ) : (
            <>
              <Alert tone="info">
                {t('reputation.window', {
                  days: status.data.window_days,
                  start: formatDate(`${status.data.window_start}T12:00:00Z`),
                  min: new Intl.NumberFormat(getLocale()).format(status.data.min_volume),
                })}
              </Alert>
              <div className="cf-split">
                {status.data.classes.map((c) => (
                  <ClassCard key={c.class} status={c} />
                ))}
              </div>
            </>
          )
        ) : null}
        {canHistory ? <HistoryCard classes={classes} /> : null}
      </div>
    </div>
  );
}

function UsageRow({ usage, label }: { usage: RateUsage; label: string }) {
  const format = new Intl.NumberFormat(getLocale());
  if (usage.used === null) {
    return (
      <span className="cf-text-sm cf-text-muted">
        {t('reputation.usageUnavailable', { limit: format.format(usage.limit) })}
      </span>
    );
  }
  return (
    <div className="cf-stack" style={{ gap: 'var(--cf-space-1)' }}>
      <span className="cf-text-sm">
        {t('reputation.usedOf', {
          used: format.format(usage.used),
          limit: format.format(usage.limit),
        })}
      </span>
      <Meter ratio={usage.limit > 0 ? usage.used / usage.limit : 0} label={label} />
    </div>
  );
}

function ClassCard({ status: c }: { status: ClassStatus }) {
  const format = new Intl.NumberFormat(getLocale());
  const rows = [
    {
      key: 'bounce',
      label: t('reputation.bounceRate'),
      rate: c.window.bounce_rate,
      warn: c.thresholds.bounce_warn,
      block: c.thresholds.bounce_block,
    },
    {
      key: 'complaint',
      label: t('reputation.complaintRate'),
      rate: c.window.complaint_rate,
      warn: c.thresholds.complaint_warn,
      block: c.thresholds.complaint_block,
    },
  ];
  const className = tEnum('sendClass', c.class);
  return (
    <Card
      title={
        <span className="cf-inline">
          {className}
          <StateBadge state={c.state} />
          {c.manual ? <Badge tone="accent">{t('reputation.manual')}</Badge> : null}
        </span>
      }
      description={reasonText(c.reason)}
    >
      <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
        <div className="cf-table-wrap">
          <table className="cf-table">
            <caption className="cf-visually-hidden">
              {t('reputation.thresholdsCaption', { class: className })}
            </caption>
            <thead>
              <tr>
                <th scope="col">{t('reputation.metric')}</th>
                <th scope="col" className="cf-table__cell--right">
                  {t('reputation.current')}
                </th>
                <th scope="col" className="cf-table__cell--right">
                  {t('reputation.warnAt')}
                </th>
                <th scope="col" className="cf-table__cell--right">
                  {t('reputation.blockAt')}
                </th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.key}>
                  <th scope="row">{r.label}</th>
                  <td className="cf-table__cell--right">{formatRate(r.rate)}</td>
                  <td className="cf-table__cell--right">{formatRate(r.warn)}</td>
                  <td className="cf-table__cell--right">{formatRate(r.block)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <DescriptionList
          items={[
            { label: t('reputation.sent'), value: format.format(c.window.sent) },
            { label: t('reputation.bounced'), value: format.format(c.window.bounced) },
            { label: t('reputation.complained'), value: format.format(c.window.complained) },
            { label: t('reputation.changedAt'), value: formatDateTime(c.changed_at) },
            {
              label: t('reputation.hourly'),
              value: <UsageRow usage={c.hourly} label={t('reputation.hourly')} />,
            },
            {
              label: t('reputation.daily'),
              value: <UsageRow usage={c.daily} label={t('reputation.daily')} />,
            },
          ]}
        />
      </div>
    </Card>
  );
}

function HistoryCard({ classes }: { classes: readonly SendClass[] }) {
  const pager = usePagination();
  const [sendClass, setSendClass] = useState<SendClass | ''>('');
  const history = useQuery(
    () =>
      reputationApi.history({
        page: pager.page,
        per_page: pager.perPage,
        class: sendClass || undefined,
      }),
    [pager.page, pager.perPage, sendClass],
  );
  const columns: Column<StateChange>[] = [
    { key: 'when', header: t('reputation.changedAt'), render: (h) => formatDateTime(h.created_at) },
    { key: 'class', header: t('reputation.class'), render: (h) => tEnum('sendClass', h.class) },
    {
      key: 'change',
      header: t('reputation.change'),
      render: (h) => (
        <span className="cf-inline">
          <StateBadge state={h.from} />
          <span aria-hidden="true">/</span>
          <span className="cf-visually-hidden">{t('reputation.to')}</span>
          <StateBadge state={h.to} />
        </span>
      ),
    },
    {
      key: 'reason',
      header: t('reputation.reason'),
      render: (h) => <span className="cf-break">{reasonText(h.reason)}</span>,
    },
    {
      key: 'bounce',
      header: t('reputation.bounceRate'),
      align: 'right',
      render: (h) => formatRate(h.bounce_rate),
    },
    {
      key: 'complaint',
      header: t('reputation.complaintRate'),
      align: 'right',
      render: (h) => formatRate(h.complaint_rate),
    },
    {
      key: 'manual',
      header: t('reputation.origin'),
      render: (h) =>
        h.manual ? (
          <Badge tone="accent">{t('reputation.manual')}</Badge>
        ) : (
          t('reputation.automatic')
        ),
    },
  ];
  return (
    <Card
      flush
      title={t('reputation.history.title')}
      description={t('reputation.history.description')}
    >
      <div className="cf-toolbar">
        <div className="cf-field">
          <label className="cf-field__label" htmlFor="reputation-history-class">
            {t('reputation.class')}
          </label>
          <Select
            id="reputation-history-class"
            placeholder={t('common.all')}
            options={classes.map((c) => ({ value: c, label: tEnum('sendClass', c) }))}
            value={sendClass}
            onChange={(e) => {
              setSendClass(e.target.value as SendClass | '');
              pager.reset();
            }}
          />
        </div>
      </div>
      <DataTable
        columns={columns}
        rows={history.data?.items ?? []}
        rowKey={(h) => h.id}
        loading={history.loading}
        error={history.error}
        onRetry={history.reload}
        empty={{ title: t('reputation.history.empty') }}
        pagination={{
          page: history.data?.page ?? pager.page,
          perPage: pager.perPage,
          total: history.data?.total ?? 0,
          totalPages: history.data?.totalPages ?? 0,
          onPageChange: pager.setPage,
        }}
      />
    </Card>
  );
}
