import { useState, type FormEvent } from 'react';
import { auditApi, RISK_LEVELS, type RiskLevel, type SecurityEvent } from '@/api/audit';
import { errorMessage } from '@/api/messages';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import {
  Badge,
  Button,
  Card,
  Checkbox,
  DataTable,
  Input,
  PageHeader,
  Select,
  useToast,
  type BadgeTone,
  type Column,
} from '@/design/components';
import { IconCheck } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { t, tEnum } from '@/i18n';
import { MissingPermission } from '@/pages/shared/MissingPermission';

function riskTone(level: RiskLevel): BadgeTone {
  if (level === 'critical' || level === 'high') return 'danger';
  if (level === 'medium') return 'warning';
  return 'info';
}

interface Filters {
  event_type: string;
  risk_level: string;
  pendingOnly: boolean;
}

const EMPTY: Filters = { event_type: '', risk_level: '', pendingOnly: true };

export default function SecurityEventsPage() {
  const { can } = useAccess();
  if (!can(...PERMISSIONS.securityEvents.read)) {
    return (
      <MissingPermission title={t('audit.events.title')} description={t('audit.events.subtitle')} />
    );
  }
  return <SecurityEventsView />;
}

function SecurityEventsView() {
  const toast = useToast();
  const { can } = useAccess();
  const pager = usePagination();
  const [draft, setDraft] = useState<Filters>(EMPTY);
  const [applied, setApplied] = useState<Filters>(EMPTY);

  const events = useQuery(
    () =>
      auditApi.listSecurityEvents({
        page: pager.page,
        per_page: pager.perPage,
        event_type: applied.event_type.trim() || undefined,
        risk_level: applied.risk_level || undefined,
        acknowledged: applied.pendingOnly ? false : undefined,
      }),
    [pager.page, pager.perPage, applied],
  );

  const acknowledge = useAction(async (event: SecurityEvent) => {
    await auditApi.acknowledge(event.id);
  });

  const apply = (e: FormEvent) => {
    e.preventDefault();
    setApplied(draft);
    pager.reset();
  };

  const canAck = can(...PERMISSIONS.securityEvents.acknowledge);

  const columns: Column<SecurityEvent>[] = [
    {
      key: 'when',
      header: t('audit.events.column.when'),
      render: (e) => formatDateTime(e.created_at),
    },
    {
      key: 'type',
      header: t('audit.events.column.type'),
      render: (e) => <span className="cf-mono">{e.event_type}</span>,
    },
    {
      key: 'risk',
      header: t('audit.events.column.risk'),
      render: (e) => (
        <Badge tone={riskTone(e.risk_level)}>{tEnum('audit.risk', e.risk_level)}</Badge>
      ),
    },
    {
      key: 'ip',
      header: t('audit.events.column.ip'),
      render: (e) => <span className="cf-mono">{e.ip_address}</span>,
    },
    { key: 'detail', header: t('audit.events.column.detail'), render: (e) => e.detail },
    {
      key: 'state',
      header: t('audit.events.column.state'),
      render: (e) =>
        e.acknowledged ? (
          <span title={formatDateTime(e.acknowledged_at)}>
            <Badge tone="success">{t('audit.events.badge.acknowledged')}</Badge>
          </span>
        ) : (
          <Badge tone="warning">{t('audit.events.badge.pending')}</Badge>
        ),
    },
    ...(canAck
      ? [
          {
            key: 'actions',
            header: '',
            align: 'right' as const,
            render: (e: SecurityEvent) =>
              e.acknowledged ? null : (
                <Button
                  size="sm"
                  icon={<IconCheck size={14} />}
                  loading={acknowledge.busy}
                  onClick={() => {
                    void acknowledge.run(e).then((ok) => {
                      if (ok) {
                        toast.success(t('audit.events.acknowledged'));
                        events.reload();
                      } else {
                        toast.error(errorMessage(acknowledge.error));
                      }
                    });
                  }}
                >
                  {t('audit.events.acknowledge')}
                </Button>
              ),
          },
        ]
      : []),
  ];

  return (
    <div>
      <PageHeader title={t('audit.events.title')} description={t('audit.events.subtitle')} />
      <Card flush>
        <form className="cf-toolbar" onSubmit={apply}>
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="events-type">
              {t('audit.events.filter.type')}
            </label>
            <Input
              id="events-type"
              className="cf-mono"
              value={draft.event_type}
              onChange={(e) => setDraft((d) => ({ ...d, event_type: e.target.value }))}
            />
          </div>
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="events-risk">
              {t('audit.events.filter.risk')}
            </label>
            <Select
              id="events-risk"
              placeholder={t('common.all')}
              options={RISK_LEVELS.map((r) => ({ value: r, label: tEnum('audit.risk', r) }))}
              value={draft.risk_level}
              onChange={(e) => setDraft((d) => ({ ...d, risk_level: e.target.value }))}
            />
          </div>
          <Checkbox
            label={t('audit.events.filter.pendingOnly')}
            checked={draft.pendingOnly}
            onChange={(e) => setDraft((d) => ({ ...d, pendingOnly: e.target.checked }))}
          />
          <div className="cf-toolbar__actions">
            <Button type="submit" variant="primary">
              {t('common.apply')}
            </Button>
          </div>
        </form>
        <DataTable
          columns={columns}
          rows={events.data?.items ?? []}
          rowKey={(e) => e.id}
          loading={events.loading}
          error={events.error}
          onRetry={events.reload}
          empty={{ title: t('audit.events.empty') }}
          pagination={{
            page: events.data?.page ?? pager.page,
            perPage: pager.perPage,
            total: events.data?.total ?? 0,
            totalPages: events.data?.totalPages ?? 0,
            onPageChange: pager.setPage,
          }}
        />
      </Card>
    </div>
  );
}
