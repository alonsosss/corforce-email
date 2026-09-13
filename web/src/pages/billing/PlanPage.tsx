import { billingApi, billingMeta, type Plan } from '@/api/billing';
import { ERROR_CODES, errorCode } from '@/api/errors';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useQuery } from '@/hooks/useQuery';
import { useResource } from '@/hooks/useResource';
import {
  Badge,
  Card,
  DataTable,
  DescriptionList,
  EmptyState,
  ErrorState,
  PageHeader,
  Skeleton,
  type Column,
} from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { t, tEnum } from '@/i18n';
import { MissingPermission } from '@/pages/shared/MissingPermission';
import { ResourceGate } from '@/pages/shared/ResourceGate';
import { formatIncluded, formatMoney, formatPeriodDay, UsageTable } from './billingFormat';
import { SubscriptionStatusBadge } from './subscriptionStatus';

export default function PlanPage() {
  const { can } = useAccess();
  const canSubscription = can(...PERMISSIONS.billingSubscription.read);
  const canUsage = can(...PERMISSIONS.billingUsage.read);
  if (!canSubscription && !canUsage) {
    return <MissingPermission title={t('billing.title')} description={t('billing.subtitle')} />;
  }
  return (
    <div>
      <PageHeader title={t('billing.title')} description={t('billing.subtitle')} />
      <div className="cf-stack">
        {canSubscription ? <SubscriptionCard /> : null}
        {canUsage ? <UsageCard /> : null}
      </div>
    </div>
  );
}

function SubscriptionCard() {
  const subscription = useQuery(async () => (await billingApi.subscription()).data, []);
  const meta = useResource(billingMeta);
  if (subscription.error) {
    return (
      <Card title={t('billing.subscription.title')}>
        {errorCode(subscription.error) === ERROR_CODES.NO_SUBSCRIPTION ? (
          <EmptyState
            title={t('billing.noSubscription')}
            description={t('billing.noSubscriptionHint')}
          />
        ) : (
          <ErrorState error={subscription.error} onRetry={subscription.reload} />
        )}
      </Card>
    );
  }
  if (!subscription.data) {
    return (
      <Card title={t('billing.subscription.title')}>
        <Skeleton lines={5} />
      </Card>
    );
  }
  const s = subscription.data;
  const plan = s.plan;
  return (
    <div className="cf-stack">
      <Card title={t('billing.subscription.title')} description={plan?.description || undefined}>
        <DescriptionList
          items={[
            { label: t('billing.plan'), value: <strong>{plan?.name ?? s.plan_code}</strong> },
            { label: t('billing.planCode'), value: <span className="cf-mono">{s.plan_code}</span> },
            { label: t('common.status'), value: <SubscriptionStatusBadge status={s.status} /> },
            {
              label: t('billing.period'),
              value: t('billing.periodRange', {
                from: formatPeriodDay(s.current_period_start),
                to: formatPeriodDay(s.current_period_end),
              }),
            },
            ...(plan
              ? [
                  {
                    label: t('billing.price'),
                    value: t('billing.pricePer', {
                      price: formatMoney(plan.base_price, plan.currency),
                      period: tEnum('billing.periodUnit', plan.billing_period),
                    }),
                  },
                ]
              : []),
            ...(s.trial_ends_at
              ? [{ label: t('billing.trialEnds'), value: formatDateTime(s.trial_ends_at) }]
              : []),
            ...(s.cancel_at
              ? [{ label: t('billing.cancelAt'), value: formatDateTime(s.cancel_at) }]
              : []),
          ]}
        />
      </Card>
      {plan ? (
        <ResourceGate resource={meta}>
          {(catalog) => <LimitsCard plan={plan} unlimited={catalog.unlimited} />}
        </ResourceGate>
      ) : null}
    </div>
  );
}

export function LimitsCard({ plan, unlimited }: { plan: Plan; unlimited: number }) {
  const columns: Column<Plan['limits'][number]>[] = [
    {
      key: 'resource',
      header: t('billing.column.resource'),
      render: (l) => tEnum('billing.resource', l.resource),
    },
    {
      key: 'included',
      header: t('billing.column.included'),
      align: 'right',
      render: (l) => formatIncluded(l.resource, l.included, unlimited),
    },
    {
      key: 'kind',
      header: t('billing.column.limitKind'),
      render: (l) =>
        l.hard_limit ? (
          <Badge tone="warning">{t('billing.hardLimit')}</Badge>
        ) : (
          <Badge tone="info">{t('billing.softLimit')}</Badge>
        ),
    },
    {
      key: 'overage',
      header: t('billing.column.overagePrice'),
      align: 'right',
      render: (l) =>
        l.overage_unit_price ? formatMoney(l.overage_unit_price, plan.currency) : t('common.dash'),
    },
  ];
  return (
    <Card flush title={t('billing.limits.title')} description={t('billing.limits.description')}>
      <DataTable
        columns={columns}
        rows={plan.limits}
        rowKey={(l) => l.resource}
        empty={{ title: t('billing.limits.empty') }}
      />
    </Card>
  );
}

function UsageCard() {
  const usage = useQuery(async () => (await billingApi.usage()).data, []);
  const meta = useResource(billingMeta);
  return (
    <Card
      flush
      title={t('billing.usage.title')}
      description={
        usage.data
          ? t('billing.periodRange', {
              from: formatPeriodDay(usage.data.period_start),
              to: formatPeriodDay(usage.data.period_end),
            })
          : undefined
      }
    >
      {usage.error ? (
        <div className="cf-table__state">
          {errorCode(usage.error) === ERROR_CODES.NO_SUBSCRIPTION ? (
            <EmptyState title={t('billing.noSubscription')} />
          ) : (
            <ErrorState error={usage.error} onRetry={usage.reload} />
          )}
        </div>
      ) : meta.error ? (
        <div className="cf-table__state">
          <ErrorState error={meta.error} onRetry={meta.reload} />
        </div>
      ) : !usage.data || !meta.data ? (
        <div className="cf-table__state">
          <Skeleton lines={5} />
        </div>
      ) : (
        <UsageTable report={usage.data} unlimited={meta.data.unlimited} />
      )}
    </Card>
  );
}
