import { useState } from 'react';
import {
  REPUTATION_STATES,
  reputationApi,
  type ClassSummary,
  type ReputationState,
} from '@/api/reputation';
import type { SendClass } from '@/api/sendClass';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import {
  Badge,
  Button,
  Card,
  ConfirmDialog,
  DataTable,
  FormField,
  Input,
  PageHeader,
  Select,
  Textarea,
  useToast,
  type Column,
} from '@/design/components';
import { formatRate } from '@/lib/decimal';
import { rules, validateField } from '@/lib/validate';
import { getLocale, t, tEnum } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { RowActions } from '@/pages/shared/RowActions';
import { tenantLabel, useTenantDirectory } from '@/pages/billing/useTenantNames';
import { reasonText, StateBadge } from './reputationState';

/** Una fila por empresa y clase; una empresa cuya base no respondio ocupa una fila sin clase. */
interface Row {
  key: string;
  tenantId: string;
  unavailable: boolean;
  summary: ClassSummary | null;
}

interface Target {
  tenantId: string;
  sendClass: SendClass;
}

type Dialog = { kind: 'limits' | 'suspend' | 'release'; target: Target } | null;

/** Reputacion de todas las empresas y sus acciones manuales: solo el superadmin. */
export default function PlatformReputationPage() {
  const toast = useToast();
  const directory = useTenantDirectory();
  const [state, setState] = useState<ReputationState | ''>('');
  const [dialog, setDialog] = useState<Dialog>(null);
  const tenants = useQuery(() => reputationApi.tenants(state || undefined), [state]);
  const format = new Intl.NumberFormat(getLocale());

  const rows: Row[] = (tenants.data ?? []).flatMap<Row>((tn) =>
    tn.unavailable || tn.classes.length === 0
      ? [{ key: tn.tenant_id, tenantId: tn.tenant_id, unavailable: tn.unavailable, summary: null }]
      : tn.classes.map((c) => ({
          key: `${tn.tenant_id}:${c.class}`,
          tenantId: tn.tenant_id,
          unavailable: false,
          summary: c,
        })),
  );

  const close = () => setDialog(null);
  const done = (message: string) => {
    toast.success(message);
    close();
    tenants.reload();
  };
  const target = dialog?.target;

  const columns: Column<Row>[] = [
    {
      key: 'tenant',
      header: t('reputation.platform.tenant'),
      render: (r) => <span className="cf-break">{tenantLabel(directory.byId, r.tenantId)}</span>,
    },
    {
      key: 'class',
      header: t('reputation.class'),
      render: (r) =>
        r.summary ? (
          tEnum('sendClass', r.summary.class)
        ) : (
          <Badge tone="warning">{t('reputation.platform.unavailable')}</Badge>
        ),
    },
    {
      key: 'state',
      header: t('common.status'),
      render: (r) =>
        r.summary ? (
          <span className="cf-inline">
            <StateBadge state={r.summary.state} />
            {r.summary.manual ? <Badge tone="accent">{t('reputation.manual')}</Badge> : null}
          </span>
        ) : (
          t('common.dash')
        ),
    },
    {
      key: 'reason',
      header: t('reputation.reason'),
      render: (r) =>
        r.summary ? (
          <span className="cf-break cf-text-sm">{reasonText(r.summary.reason)}</span>
        ) : (
          t('common.dash')
        ),
    },
    {
      key: 'sent',
      header: t('reputation.sent'),
      align: 'right',
      render: (r) => (r.summary ? format.format(r.summary.window.sent) : t('common.dash')),
    },
    {
      key: 'bounce',
      header: t('reputation.bounceRate'),
      align: 'right',
      render: (r) => (r.summary ? formatRate(r.summary.window.bounce_rate) : t('common.dash')),
    },
    {
      key: 'complaint',
      header: t('reputation.complaintRate'),
      align: 'right',
      render: (r) => (r.summary ? formatRate(r.summary.window.complaint_rate) : t('common.dash')),
    },
    {
      key: 'actions',
      header: '',
      align: 'right',
      render: (r) => {
        const s = r.summary;
        if (!s) return null;
        const tgt = { tenantId: r.tenantId, sendClass: s.class };
        return (
          <RowActions>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => setDialog({ kind: 'limits', target: tgt })}
            >
              {t('reputation.platform.limits')}
            </Button>
            {s.state !== 'suspended' ? (
              <Button
                size="sm"
                variant="ghost"
                onClick={() => setDialog({ kind: 'suspend', target: tgt })}
              >
                {t('reputation.platform.suspend')}
              </Button>
            ) : null}
            {s.manual ? (
              <Button
                size="sm"
                variant="ghost"
                onClick={() => setDialog({ kind: 'release', target: tgt })}
              >
                {t('reputation.platform.release')}
              </Button>
            ) : null}
          </RowActions>
        );
      },
    },
  ];

  const targetName = target
    ? t('reputation.platform.target', {
        tenant: tenantLabel(directory.byId, target.tenantId),
        class: tEnum('sendClass', target.sendClass),
      })
    : '';

  return (
    <div>
      <PageHeader
        title={t('reputation.platform.title')}
        description={t('reputation.platform.subtitle')}
      />
      <Card flush>
        <div className="cf-toolbar">
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="reputation-state">
              {t('reputation.platform.stateFilter')}
            </label>
            <Select
              id="reputation-state"
              placeholder={t('common.all')}
              options={REPUTATION_STATES.map((s) => ({
                value: s,
                label: tEnum('reputation.state', s),
              }))}
              value={state}
              onChange={(e) => setState(e.target.value as ReputationState | '')}
            />
          </div>
        </div>
        <DataTable
          columns={columns}
          rows={rows}
          rowKey={(r) => r.key}
          loading={tenants.loading}
          error={tenants.error}
          onRetry={tenants.reload}
          empty={{ title: t('reputation.platform.empty') }}
        />
      </Card>
      {dialog?.kind === 'limits' && target ? (
        <LimitsForm target={target} title={targetName} onClose={close} onDone={done} />
      ) : null}
      {dialog?.kind === 'suspend' && target ? (
        <SuspendForm target={target} title={targetName} onClose={close} onDone={done} />
      ) : null}
      <ConfirmDialog
        open={dialog?.kind === 'release'}
        title={t('reputation.platform.release')}
        message={t('reputation.platform.releaseConfirm', { target: targetName })}
        confirmLabel={t('reputation.platform.release')}
        onCancel={close}
        onConfirm={async () => {
          if (!target) return;
          await reputationApi.release(target.tenantId, target.sendClass);
          done(t('reputation.platform.released'));
        }}
      />
    </div>
  );
}

const INTEGER = /^\d+$/;

function LimitsForm({
  target,
  title,
  onClose,
  onDone,
}: {
  target: Target;
  title: string;
  onClose: () => void;
  onDone: (message: string) => void;
}) {
  const [hourly, setHourly] = useState('');
  const [daily, setDaily] = useState('');
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});
  const format = new Intl.NumberFormat(getLocale());
  const action = useAction(async (h: number | null, d: number | null) => {
    const { data } = await reputationApi.setLimits(target.tenantId, target.sendClass, {
      hourly: h,
      daily: d,
    });
    onDone(
      t('reputation.platform.limitsSaved', {
        hourly: format.format(data.hourly),
        daily: format.format(data.daily),
      }),
    );
  });
  const parse = (v: string) => (v.trim() ? Number(v) : null);
  return (
    <FormModal
      id="reputation-limits"
      title={t('reputation.platform.limits')}
      submitLabel={t('common.save')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={async () => {
        const next = {
          hourly:
            hourly.trim() && !INTEGER.test(hourly.trim())
              ? t('validation.nonNegativeInteger')
              : undefined,
          daily:
            daily.trim() && !INTEGER.test(daily.trim())
              ? t('validation.nonNegativeInteger')
              : undefined,
        };
        setErrors(next);
        if (next.hourly || next.daily) return;
        await action.run(parse(hourly), parse(daily));
      }}
    >
      <p className="cf-text-secondary">{title}</p>
      <span className="cf-text-sm cf-text-secondary">{t('reputation.platform.limitsHint')}</span>
      <div className="cf-form__row">
        <FormField label={t('reputation.hourly')} htmlFor="reputation-hourly" error={errors.hourly}>
          <Input
            id="reputation-hourly"
            inputMode="numeric"
            value={hourly}
            onChange={(e) => setHourly(e.target.value)}
            invalid={Boolean(errors.hourly)}
          />
        </FormField>
        <FormField label={t('reputation.daily')} htmlFor="reputation-daily" error={errors.daily}>
          <Input
            id="reputation-daily"
            inputMode="numeric"
            value={daily}
            onChange={(e) => setDaily(e.target.value)}
            invalid={Boolean(errors.daily)}
          />
        </FormField>
      </div>
    </FormModal>
  );
}

function SuspendForm({
  target,
  title,
  onClose,
  onDone,
}: {
  target: Target;
  title: string;
  onClose: () => void;
  onDone: (message: string) => void;
}) {
  const [reason, setReason] = useState('');
  const [error, setError] = useState<string | null>(null);
  const action = useAction(async () => {
    await reputationApi.suspend(target.tenantId, target.sendClass, reason.trim());
    onDone(t('reputation.platform.suspended'));
  });
  return (
    <FormModal
      id="reputation-suspend"
      title={t('reputation.platform.suspend')}
      submitLabel={t('reputation.platform.suspend')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={async () => {
        const next = validateField(reason, rules.required);
        setError(next);
        if (!next) await action.run();
      }}
    >
      <p className="cf-text-secondary">{title}</p>
      <span className="cf-text-sm cf-text-secondary">{t('reputation.platform.suspendHint')}</span>
      <FormField
        label={t('reputation.reason')}
        htmlFor="reputation-suspend-reason"
        required
        error={error}
      >
        <Textarea
          id="reputation-suspend-reason"
          rows={3}
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          invalid={Boolean(error)}
        />
      </FormField>
    </FormModal>
  );
}
