import { useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import {
  campaignsApi,
  type Campaign,
  type CampaignBatch,
  type CampaignRates,
  type CampaignStats,
  type TestSendResult,
} from '@/api/campaigns';
import { MODULES } from '@/access/modules';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import {
  Alert,
  Button,
  Card,
  ChipsInput,
  ConfirmDialog,
  DataTable,
  DescriptionList,
  ErrorState,
  FormField,
  Input,
  PageHeader,
  Skeleton,
  useToast,
  type Column,
} from '@/design/components';
import {
  IconCalendar,
  IconEdit,
  IconPause,
  IconPlay,
  IconRefresh,
  IconSend,
  IconStop,
  IconTrash,
} from '@/design/icons';
import { isPast } from '@/lib/datetime';
import { formatRate } from '@/lib/decimal';
import { formatDateTime, localToRfc3339 } from '@/lib/format';
import { rules } from '@/lib/validate';
import { getLocale, t, tEnum } from '@/i18n';
import { paths } from '@/paths';
import { FormModal } from '@/pages/shared/FormModal';
import { CampaignForm } from './CampaignForm';
import { loadCampaignOptions, nameOf, type CampaignOptions } from './campaignOptions';
import { describeReason } from './campaignReason';
import { campaignRules, MAX_TEST_RECIPIENTS } from './campaignRules';
import { BatchStatusBadge, CampaignStatusBadge } from './campaignStatus';

type Dialog =
  'edit' | 'schedule' | 'start' | 'pause' | 'resume' | 'cancel' | 'delete' | 'test' | null;

const COUNTERS: (keyof Omit<CampaignStats, 'rates'>)[] = [
  'targeted',
  'accepted',
  'suppressed',
  'sent',
  'delivered',
  'bounced',
  'complained',
  'opened',
  'clicked',
  'unsubscribed',
  'failed',
];
const RATES: (keyof CampaignRates)[] = [
  'delivery_rate',
  'open_rate',
  'click_rate',
  'bounce_rate',
  'complaint_rate',
  'unsubscribe_rate',
];

export default function CampaignDetailPage() {
  const { id = '' } = useParams();
  const navigate = useNavigate();
  const toast = useToast();
  const { can, hasModule } = useAccess();
  const [dialog, setDialog] = useState<Dialog>(null);
  const campaign = useQuery(async () => (await campaignsApi.get(id)).data, [id]);
  const access = {
    templates: can(...PERMISSIONS.templates.read),
    lists: can(...PERMISSIONS.contactLists.read),
    segments: can(...PERMISSIONS.segments.read),
  };
  const options = useQuery(
    () => loadCampaignOptions(access),
    [access.templates, access.lists, access.segments],
  );

  if (campaign.error) {
    return (
      <div>
        <PageHeader
          title={t('campaigns.title')}
          back={{ to: paths.campaigns, label: t('nav.campaigns') }}
        />
        <Card>
          <ErrorState
            error={campaign.error}
            title={t('campaigns.notFound')}
            onRetry={campaign.reload}
          />
        </Card>
      </div>
    );
  }
  if (!campaign.data) {
    return (
      <Card>
        <Skeleton lines={8} />
      </Card>
    );
  }

  const c = campaign.data;
  const close = () => setDialog(null);
  const applied = (next: Campaign, message: string) => {
    campaign.setData(next);
    toast.success(message);
    close();
  };
  const canSend = can(...PERMISSIONS.campaigns.send);
  const canCancel = can(...PERMISSIONS.campaigns.cancel);
  const pause = describeReason(c.pause_reason);
  const failure = describeReason(c.failure_reason);

  return (
    <div>
      <PageHeader
        title={c.name}
        description={
          <span className="cf-inline">
            <CampaignStatusBadge status={c.status} />
            {c.template_version ? (
              <span>{t('templates.versionLabel', { n: c.template_version })}</span>
            ) : null}
          </span>
        }
        back={{ to: paths.campaigns, label: t('nav.campaigns') }}
        actions={
          <>
            <Button variant="ghost" icon={<IconRefresh size={16} />} onClick={campaign.reload}>
              {t('common.refresh')}
            </Button>
            {can(...PERMISSIONS.campaigns.update) && campaignRules.editable(c.status) ? (
              <Button icon={<IconEdit size={16} />} onClick={() => setDialog('edit')}>
                {t('common.edit')}
              </Button>
            ) : null}
            {canSend ? (
              <Button icon={<IconSend size={16} />} onClick={() => setDialog('test')}>
                {t('campaigns.test.action')}
              </Button>
            ) : null}
            {canSend && campaignRules.schedulable(c.status) ? (
              <Button icon={<IconCalendar size={16} />} onClick={() => setDialog('schedule')}>
                {t('campaigns.schedule.action')}
              </Button>
            ) : null}
            {canSend && campaignRules.startable(c.status) ? (
              <Button
                variant="primary"
                icon={<IconPlay size={16} />}
                onClick={() => setDialog('start')}
              >
                {t('campaigns.start.action')}
              </Button>
            ) : null}
            {canCancel && campaignRules.pausable(c.status) ? (
              <Button icon={<IconPause size={16} />} onClick={() => setDialog('pause')}>
                {t('campaigns.pause.action')}
              </Button>
            ) : null}
            {canSend && campaignRules.resumable(c.status) ? (
              <Button
                variant="primary"
                icon={<IconPlay size={16} />}
                onClick={() => setDialog('resume')}
              >
                {t('campaigns.resume.action')}
              </Button>
            ) : null}
            {canCancel && campaignRules.cancellable(c.status) ? (
              <Button
                variant="danger"
                icon={<IconStop size={16} />}
                onClick={() => setDialog('cancel')}
              >
                {t('campaigns.cancel.action')}
              </Button>
            ) : null}
            {can(...PERMISSIONS.campaigns.delete) && campaignRules.deletable(c.status) ? (
              <Button
                variant="danger"
                icon={<IconTrash size={16} />}
                onClick={() => setDialog('delete')}
              >
                {t('common.delete')}
              </Button>
            ) : null}
          </>
        }
      />
      <div className="cf-stack">
        {c.status === 'paused' && pause ? (
          <Alert tone="warning" title={t('campaigns.reason.pausedTitle')}>
            <span>{pause.text}</span>
            {pause.detail ? <span className="cf-text-sm">{pause.detail}</span> : null}
          </Alert>
        ) : null}
        {c.status === 'failed' && failure ? (
          <Alert tone="danger" title={t('campaigns.reason.failedTitle')}>
            <span>{failure.text}</span>
            {failure.detail ? <span className="cf-text-sm">{failure.detail}</span> : null}
          </Alert>
        ) : null}
        {c.resume_after && !isPast(c.resume_after) ? (
          <Alert tone="info">
            {t('campaigns.resumeAfter', { date: formatDateTime(c.resume_after) })}
          </Alert>
        ) : null}
        <SummaryCard
          campaign={c}
          options={options.data}
          showTemplateLink={hasModule(MODULES.templates)}
        />
        {c.stats ? <StatsCard stats={c.stats} /> : null}
        {can(...PERMISSIONS.campaignStats.read) ? (
          <BatchesCard campaignId={c.id} version={c.updated_at} />
        ) : null}
      </div>

      {dialog === 'edit' ? (
        <CampaignForm
          campaign={c}
          onClose={close}
          onSaved={(next) => applied(next, t('campaigns.updated'))}
        />
      ) : null}
      {dialog === 'schedule' ? (
        <ScheduleForm
          campaign={c}
          onClose={close}
          onDone={(next) => applied(next, t('campaigns.schedule.done'))}
        />
      ) : null}
      {dialog === 'start' ? (
        <StartForm
          campaign={c}
          onClose={close}
          onDone={(next) => applied(next, t('campaigns.start.done'))}
        />
      ) : null}
      {dialog === 'test' ? <TestForm campaign={c} onClose={close} /> : null}
      <ConfirmDialog
        open={dialog === 'pause'}
        title={t('campaigns.pause.action')}
        message={t('campaigns.pause.confirm', { name: c.name })}
        confirmLabel={t('campaigns.pause.action')}
        onCancel={close}
        onConfirm={async () =>
          applied((await campaignsApi.pause(c.id)).data, t('campaigns.pause.done'))
        }
      />
      <ConfirmDialog
        open={dialog === 'resume'}
        title={t('campaigns.resume.action')}
        message={t('campaigns.resume.confirm', { name: c.name })}
        confirmLabel={t('campaigns.resume.action')}
        onCancel={close}
        onConfirm={async () =>
          applied((await campaignsApi.resume(c.id)).data, t('campaigns.resume.done'))
        }
      />
      <ConfirmDialog
        open={dialog === 'cancel'}
        title={t('campaigns.cancel.action')}
        message={t('campaigns.cancel.confirm', { name: c.name })}
        confirmLabel={t('campaigns.cancel.action')}
        danger
        onCancel={close}
        onConfirm={async () =>
          applied((await campaignsApi.cancel(c.id)).data, t('campaigns.cancel.done'))
        }
      />
      <ConfirmDialog
        open={dialog === 'delete'}
        title={t('campaigns.delete')}
        message={t('campaigns.deleteConfirm', { name: c.name })}
        confirmLabel={t('common.delete')}
        danger
        onCancel={close}
        onConfirm={async () => {
          await campaignsApi.remove(c.id);
          toast.success(t('campaigns.deleted'));
          navigate(paths.campaigns, { replace: true });
        }}
      />
    </div>
  );
}

function SummaryCard({
  campaign: c,
  options,
  showTemplateLink,
}: {
  campaign: Campaign;
  options: CampaignOptions | null;
  showTemplateLink: boolean;
}) {
  const template = options?.templates?.find((tpl) => tpl.id === c.template_id);
  const names = (ids: string[], from: CampaignOptions['lists']) =>
    ids.length ? ids.map((id) => nameOf(from, id)).join(', ') : t('common.none');
  return (
    <Card title={t('campaigns.detail.summary')}>
      <DescriptionList
        items={[
          ...(c.description ? [{ label: t('common.description'), value: c.description }] : []),
          {
            label: t('campaigns.column.template'),
            value: showTemplateLink ? (
              <Link to={paths.template(c.template_id)}>{template?.name ?? c.template_id}</Link>
            ) : (
              <span className="cf-mono">{template?.name ?? c.template_id}</span>
            ),
          },
          {
            label: t('campaigns.form.fromEmail'),
            value: (
              <span className="cf-mono">
                {c.from_name ? `${c.from_name} <${c.from_email}>` : c.from_email}
              </span>
            ),
          },
          { label: t('campaigns.form.replyTo'), value: c.reply_to || t('common.dash') },
          {
            label: t('campaigns.audience.lists'),
            value: names(c.audience.list_ids, options?.lists ?? null),
          },
          {
            label: t('campaigns.audience.segments'),
            value: names(c.audience.segment_ids, options?.segments ?? null),
          },
          {
            label: t('campaigns.audience.exclude'),
            value: names(c.audience.exclude_segment_ids, options?.segments ?? null),
          },
          { label: t('campaigns.detail.scheduledAt'), value: formatDateTime(c.scheduled_at) },
          { label: t('campaigns.detail.startedAt'), value: formatDateTime(c.started_at) },
          { label: t('campaigns.detail.completedAt'), value: formatDateTime(c.completed_at) },
          { label: t('common.createdAt'), value: formatDateTime(c.created_at) },
        ]}
      />
    </Card>
  );
}

function StatsCard({ stats }: { stats: CampaignStats }) {
  const format = new Intl.NumberFormat(getLocale());
  return (
    <Card title={t('campaigns.stats.title')} description={t('campaigns.stats.description')}>
      <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
        <div className="cf-kpis">
          {RATES.map((key) => (
            <div key={key} className="cf-kpi">
              <span className="cf-kpi__label">{tEnum('campaigns.rates', key)}</span>
              <span className="cf-kpi__value">{formatRate(stats.rates[key])}</span>
            </div>
          ))}
        </div>
        <div className="cf-kpis">
          {COUNTERS.map((key) => (
            <div key={key} className="cf-kpi">
              <span className="cf-kpi__label">{tEnum('campaigns.stats', key)}</span>
              <span className="cf-kpi__value">{format.format(stats[key])}</span>
            </div>
          ))}
        </div>
      </div>
    </Card>
  );
}

function BatchesCard({ campaignId, version }: { campaignId: string; version: string }) {
  const pager = usePagination();
  const format = new Intl.NumberFormat(getLocale());
  const batches = useQuery(
    () => campaignsApi.batches(campaignId, { page: pager.page, per_page: pager.perPage }),
    [campaignId, version, pager.page, pager.perPage],
  );
  const columns: Column<CampaignBatch>[] = [
    {
      key: 'seq',
      header: t('campaigns.batches.seq'),
      align: 'right',
      render: (b) => format.format(b.seq),
    },
    {
      key: 'status',
      header: t('common.status'),
      render: (b) => <BatchStatusBadge status={b.status} />,
    },
    {
      key: 'recipients',
      header: t('campaigns.batches.recipients'),
      align: 'right',
      render: (b) => format.format(b.recipients),
    },
    {
      key: 'accepted',
      header: t('campaigns.stats.accepted'),
      align: 'right',
      render: (b) => format.format(b.accepted),
    },
    {
      key: 'suppressed',
      header: t('campaigns.stats.suppressed'),
      align: 'right',
      render: (b) => format.format(b.suppressed),
    },
    {
      key: 'attempts',
      header: t('campaigns.batches.attempts'),
      align: 'right',
      render: (b) => format.format(b.attempts),
    },
    {
      key: 'error',
      header: t('campaigns.batches.lastError'),
      render: (b) =>
        b.last_error ? (
          <span className="cf-text-sm cf-break">{b.last_error}</span>
        ) : (
          t('common.dash')
        ),
    },
    { key: 'updated', header: t('common.updatedAt'), render: (b) => formatDateTime(b.updated_at) },
  ];
  return (
    <Card
      flush
      title={t('campaigns.batches.title')}
      description={t('campaigns.batches.description')}
    >
      <DataTable
        columns={columns}
        rows={batches.data?.items ?? []}
        rowKey={(b) => b.id}
        loading={batches.loading}
        error={batches.error}
        onRetry={batches.reload}
        empty={{ title: t('campaigns.batches.empty') }}
        pagination={{
          page: batches.data?.page ?? pager.page,
          perPage: pager.perPage,
          total: batches.data?.total ?? 0,
          totalPages: batches.data?.totalPages ?? 0,
          onPageChange: pager.setPage,
        }}
      />
    </Card>
  );
}

/** Version opcional: vacia fija la publicada en ese momento. */
function parseVersion(raw: string): number | undefined | null {
  if (!raw.trim()) return undefined;
  const n = Number(raw);
  return Number.isInteger(n) && n >= 1 ? n : null;
}

function VersionField({
  id,
  value,
  onChange,
  error,
}: {
  id: string;
  value: string;
  onChange: (v: string) => void;
  error?: string;
}) {
  return (
    <FormField
      label={t('campaigns.form.version')}
      htmlFor={id}
      error={error}
      hint={t('campaigns.form.versionHint')}
    >
      <Input
        id={id}
        type="number"
        min={1}
        step={1}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        invalid={Boolean(error)}
      />
    </FormField>
  );
}

function ScheduleForm({
  campaign,
  onClose,
  onDone,
}: {
  campaign: Campaign;
  onClose: () => void;
  onDone: (c: Campaign) => void;
}) {
  const [when, setWhen] = useState('');
  const [version, setVersion] = useState('');
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});
  const action = useAction(async (at: string, v: number | undefined) => {
    onDone((await campaignsApi.schedule(campaign.id, at, v)).data);
  });
  return (
    <FormModal
      id="campaign-schedule"
      title={t('campaigns.schedule.action')}
      submitLabel={t('campaigns.schedule.submit')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={async () => {
        const at = localToRfc3339(when);
        const v = parseVersion(version);
        const next = {
          when: !at
            ? t('validation.required')
            : isPast(at)
              ? t('campaigns.schedule.past')
              : undefined,
          version: v === null ? t('campaigns.form.versionInvalid') : undefined,
        };
        setErrors(next);
        if (next.when || next.version || !at || v === null) return;
        await action.run(at, v);
      }}
    >
      <p className="cf-text-secondary">{t('campaigns.schedule.description')}</p>
      <FormField
        label={t('campaigns.schedule.when')}
        htmlFor="campaign-schedule-when"
        required
        error={errors.when}
      >
        <Input
          id="campaign-schedule-when"
          type="datetime-local"
          value={when}
          onChange={(e) => setWhen(e.target.value)}
          invalid={Boolean(errors.when)}
        />
      </FormField>
      <VersionField
        id="campaign-schedule-version"
        value={version}
        onChange={setVersion}
        error={errors.version}
      />
    </FormModal>
  );
}

function StartForm({
  campaign,
  onClose,
  onDone,
}: {
  campaign: Campaign;
  onClose: () => void;
  onDone: (c: Campaign) => void;
}) {
  const [version, setVersion] = useState('');
  const [error, setError] = useState<string | undefined>();
  const action = useAction(async (v: number | undefined) => {
    onDone((await campaignsApi.start(campaign.id, v)).data);
  });
  return (
    <FormModal
      id="campaign-start"
      title={t('campaigns.start.action')}
      submitLabel={t('campaigns.start.submit')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={async () => {
        const v = parseVersion(version);
        setError(v === null ? t('campaigns.form.versionInvalid') : undefined);
        if (v !== null) await action.run(v);
      }}
    >
      <Alert tone="warning">{t('campaigns.start.confirm', { name: campaign.name })}</Alert>
      <VersionField
        id="campaign-start-version"
        value={version}
        onChange={setVersion}
        error={error}
      />
    </FormModal>
  );
}

function TestForm({ campaign, onClose }: { campaign: Campaign; onClose: () => void }) {
  const [emails, setEmails] = useState<string[]>([]);
  const [version, setVersion] = useState('');
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});
  const [result, setResult] = useState<TestSendResult | null>(null);
  const format = new Intl.NumberFormat(getLocale());
  const action = useAction(async (list: string[], v: number | undefined) => {
    setResult((await campaignsApi.sendTest(campaign.id, list, v)).data);
  });
  const suppressed = result?.suppressed ?? [];
  return (
    <FormModal
      id="campaign-test"
      title={t('campaigns.test.action')}
      submitLabel={t('campaigns.test.submit')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={async () => {
        const v = parseVersion(version);
        const next = {
          emails:
            emails.length === 0 || emails.length > MAX_TEST_RECIPIENTS
              ? t('campaigns.test.count', { n: MAX_TEST_RECIPIENTS })
              : undefined,
          version: v === null ? t('campaigns.form.versionInvalid') : undefined,
        };
        setErrors(next);
        if (next.emails || next.version || v === null) return;
        setResult(null);
        await action.run(emails, v);
      }}
    >
      <p className="cf-text-secondary">{t('campaigns.test.description')}</p>
      <FormField
        label={t('campaigns.test.emails')}
        htmlFor="campaign-test-emails"
        required
        error={errors.emails}
        hint={t('campaigns.test.count', { n: MAX_TEST_RECIPIENTS })}
      >
        <ChipsInput
          id="campaign-test-emails"
          values={emails}
          onChange={setEmails}
          normalize={(raw) => (rules.email(raw) === null ? raw.trim() : null)}
          invalid={Boolean(errors.emails)}
          removeLabel={(value) => t('common.removeValue', { value })}
          rejectedLabel={(list) => t('validation.invalidAddresses', { list: list.join(', ') })}
        />
      </FormField>
      <VersionField
        id="campaign-test-version"
        value={version}
        onChange={setVersion}
        error={errors.version}
      />
      {result ? (
        <Alert tone={suppressed.length ? 'warning' : 'success'} title={t('campaigns.test.done')}>
          <span>{t('campaigns.test.accepted', { n: format.format(result.accepted) })}</span>
          {suppressed.map((s) => (
            <span key={s.email} className="cf-text-sm">
              {t('campaigns.test.suppressed', { email: s.email, reason: s.reason })}
            </span>
          ))}
        </Alert>
      ) : null}
    </FormModal>
  );
}
