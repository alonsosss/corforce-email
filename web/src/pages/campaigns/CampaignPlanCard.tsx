import { campaignsApi, type Campaign, type CampaignPhase } from '@/api/campaigns';
import {
  Alert,
  Badge,
  Card,
  DataTable,
  ErrorState,
  Skeleton,
  type Column,
} from '@/design/components';
import { useQuery } from '@/hooks/useQuery';
import { formatRate } from '@/lib/decimal';
import { formatDateTime } from '@/lib/format';
import { getLocale, t, tEnum, type MessageKey } from '@/i18n';
import {
  abState,
  decisionAt,
  resendView,
  variantRows,
  zoneSlots,
  type ResendState,
  type VariantRow,
} from './campaignDelivery';

const RESEND_STATE: Record<ResendState, MessageKey> = {
  off: 'campaigns.resend.state.off',
  waitingInitial: 'campaigns.resend.state.waitingInitial',
  scheduled: 'campaigns.resend.state.scheduled',
  sending: 'campaigns.resend.state.sending',
  done: 'campaigns.resend.state.done',
};

/** Cierto si la campana tiene algo que mostrar en el plan de envio. */
export function hasDeliveryPlan(c: Campaign): boolean {
  return Boolean(c.ab_test || c.resend || c.timezone_delivery);
}

/**
 * Prueba A/B, reenvio y tramos por zona: lo que el plan de envio de la campana tiene de
 * cada uno (GET /campaigns/{id}/phases). Solo con campaigns/stats/read.
 */
export function CampaignPlanCard({ campaign: c }: { campaign: Campaign }) {
  const plan = useQuery(() => campaignsApi.plan(c.id), [c.id, c.updated_at]);
  if (plan.error) {
    return (
      <Card title={t('campaigns.plan.title')}>
        <ErrorState error={plan.error} onRetry={plan.reload} />
      </Card>
    );
  }
  if (!plan.data) {
    return (
      <Card title={t('campaigns.plan.title')}>
        <Skeleton lines={4} />
      </Card>
    );
  }
  return (
    <>
      {c.ab_test ? <ABCard campaign={c} rows={variantRows(c, plan.data)} plan={plan.data} /> : null}
      {c.resend ? <ResendCard campaign={c} plan={plan.data} /> : null}
      {c.timezone_delivery ? <ZoneCard campaign={c} slots={zoneSlots(plan.data)} /> : null}
    </>
  );
}

function ABCard({
  campaign: c,
  rows,
  plan,
}: {
  campaign: Campaign;
  rows: VariantRow[];
  plan: Parameters<typeof abState>[1];
}) {
  const format = new Intl.NumberFormat(getLocale());
  const state = abState(c, plan);
  const at = decisionAt(plan);
  const ab = c.ab_test;
  if (!ab) return null;
  const count = (n: number | undefined) => (n === undefined ? t('common.dash') : format.format(n));
  const columns: Column<VariantRow>[] = [
    {
      key: 'label',
      header: t('campaigns.ab.variant'),
      render: (r) => (
        <span className="cf-inline">
          <strong>{r.label}</strong>
          {r.winner ? <Badge tone="success">{t('campaigns.ab.winner')}</Badge> : null}
        </span>
      ),
    },
    {
      key: 'subject',
      header: t('campaigns.ab.subject'),
      render: (r) => r.subject || t('campaigns.ab.templateSubject'),
    },
    {
      key: 'accepted',
      header: t('campaigns.stats.accepted'),
      align: 'right',
      render: (r) => count(r.engagement?.accepted),
    },
    {
      key: 'delivered',
      header: t('campaigns.stats.delivered'),
      align: 'right',
      render: (r) => count(r.engagement?.delivered),
    },
    {
      key: 'open',
      header: t('campaigns.rates.open_rate'),
      align: 'right',
      render: (r) => (r.engagement ? formatRate(r.engagement.open_rate) : t('common.dash')),
    },
    {
      key: 'click',
      header: t('campaigns.rates.click_rate'),
      align: 'right',
      render: (r) => (r.engagement ? formatRate(r.engagement.click_rate) : t('common.dash')),
    },
  ];
  return (
    <Card
      flush
      title={t('campaigns.ab.title')}
      description={t('campaigns.ab.summary', {
        criterion: tEnum('campaigns.ab.criterionOption', ab.criterion),
        percent: ab.sample_percent,
        hours: Math.round(ab.decision_window_minutes / 60),
      })}
    >
      <div className="cf-stack" style={{ padding: 'var(--cf-space-4)' }}>
        <Alert tone={state === 'decided' ? 'success' : 'info'}>
          {state === 'decided' && c.ab_decided_at
            ? t('campaigns.ab.state.decided', {
                label: rows.find((r) => r.winner)?.label ?? '',
                date: formatDateTime(c.ab_decided_at),
                reason: c.ab_decision
                  ? tEnum('campaigns.ab.reason', c.ab_decision.reason)
                  : t('common.dash'),
              })
            : state === 'deciding' && at
              ? t('campaigns.ab.state.deciding', { date: formatDateTime(at) })
              : tEnum('campaigns.ab.state', state)}
        </Alert>
        <span className="cf-text-sm cf-text-secondary">{t('campaigns.ab.appleNote')}</span>
      </div>
      <DataTable
        columns={columns}
        rows={rows}
        rowKey={(r) => r.label}
        empty={{ title: t('campaigns.ab.noResults') }}
      />
    </Card>
  );
}

function ResendCard({
  campaign: c,
  plan,
}: {
  campaign: Campaign;
  plan: Parameters<typeof resendView>[1];
}) {
  const view = resendView(c, plan);
  const format = new Intl.NumberFormat(getLocale());
  if (!c.resend) return null;
  const when = view.phase?.not_before ? formatDateTime(view.phase.not_before) : '';
  return (
    <Card
      title={t('campaigns.resend.title')}
      description={t('campaigns.resend.summary', {
        subject: c.resend.subject,
        hours: Math.round(c.resend.delay_minutes / 60),
      })}
    >
      <div className="cf-stack">
        <Alert tone={view.state === 'done' ? 'success' : 'info'}>
          {t(RESEND_STATE[view.state], { date: when })}
        </Alert>
        {view.phase && view.phase.started_at ? (
          <div className="cf-kpis">
            {(
              [
                ['targeted', view.phase.targeted],
                ['accepted', view.phase.accepted],
                ['suppressed', view.phase.suppressed],
                ['opened', view.engagement?.opened ?? 0],
                ['clicked', view.engagement?.clicked ?? 0],
              ] as const
            ).map(([key, value]) => (
              <div key={key} className="cf-kpi">
                <span className="cf-kpi__label">{tEnum('campaigns.stats', key)}</span>
                <span className="cf-kpi__value">{format.format(value)}</span>
              </div>
            ))}
          </div>
        ) : null}
        <span className="cf-text-sm cf-text-secondary">{t('campaigns.resend.appleNote')}</span>
      </div>
    </Card>
  );
}

function ZoneCard({ campaign: c, slots }: { campaign: Campaign; slots: CampaignPhase[] }) {
  const format = new Intl.NumberFormat(getLocale());
  const tz = c.timezone_delivery;
  if (!tz) return null;
  const columns: Column<CampaignPhase>[] = [
    { key: 'slot', header: t('campaigns.zone.slot'), render: (p) => formatDateTime(p.slot_at) },
    {
      key: 'status',
      header: t('common.status'),
      render: (p) => (
        <Badge tone={p.status === 'done' ? 'success' : p.started_at ? 'info' : 'neutral'}>
          {p.status === 'done'
            ? t('campaigns.zone.done')
            : p.started_at
              ? t('campaigns.zone.sending')
              : t('campaigns.zone.pending')}
        </Badge>
      ),
    },
    {
      key: 'targeted',
      header: t('campaigns.batches.recipients'),
      align: 'right',
      render: (p) => format.format(p.targeted),
    },
    {
      key: 'accepted',
      header: t('campaigns.stats.accepted'),
      align: 'right',
      render: (p) => format.format(p.accepted),
    },
    {
      key: 'suppressed',
      header: t('campaigns.stats.suppressed'),
      align: 'right',
      render: (p) => format.format(p.suppressed),
    },
  ];
  return (
    <Card
      flush
      title={t('campaigns.zone.title')}
      description={t('campaigns.zone.summary', {
        local: tz.local_send_at.replace('T', ' '),
        zone: tz.fallback_timezone,
      })}
    >
      <DataTable
        columns={columns}
        rows={slots}
        rowKey={(p) => p.id}
        empty={{ title: t('campaigns.zone.empty') }}
      />
    </Card>
  );
}
