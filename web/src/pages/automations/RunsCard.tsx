import { useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import {
  automationsApi,
  type AutomationsMeta,
  type RunStatus,
  type Workflow,
  type WorkflowRun,
} from '@/api/automations';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import { Button, Card, DataTable, Select, type Column } from '@/design/components';
import { IconRefresh } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { getLocale, t, tEnum } from '@/i18n';
import { paths } from '@/paths';
import { describeRunError } from './automationReason';
import { RunStatusBadge } from './automationStatus';

/** Posicion de la ejecucion en el flujo: "2 de 5" y el tipo del paso si se conoce. */
export function stepLabel(run: WorkflowRun, workflow: Workflow | null): string {
  const steps = workflow?.steps ?? [];
  const position = t('automations.runs.stepOf', {
    n: run.step_index + 1,
    total: steps.length || t('common.dash'),
  });
  const step = steps[run.step_index];
  return step ? `${position}: ${tEnum('automations.stepType', step.type)}` : position;
}

/** Ejecuciones de un flujo, una por contacto que entro, con su paso y su resultado. */
export function RunsCard({ workflow, meta }: { workflow: Workflow; meta: AutomationsMeta }) {
  const navigate = useNavigate();
  const { can } = useAccess();
  const pager = usePagination();
  const [status, setStatus] = useState<RunStatus | ''>('');
  const format = new Intl.NumberFormat(getLocale());
  const canContacts = can(...PERMISSIONS.contacts.read);
  const runs = useQuery(
    () =>
      automationsApi.listRuns(workflow.id, {
        page: pager.page,
        per_page: pager.perPage,
        status: status || undefined,
      }),
    [workflow.id, workflow.updated_at, pager.page, pager.perPage, status],
  );

  const columns: Column<WorkflowRun>[] = [
    {
      key: 'contact',
      header: t('automations.runs.contact'),
      render: (r) =>
        canContacts ? (
          <Link
            to={paths.contact(r.contact_id)}
            className="cf-mono cf-text-sm"
            onClick={(e) => e.stopPropagation()}
          >
            {r.contact_id}
          </Link>
        ) : (
          <span className="cf-mono cf-text-sm">{r.contact_id}</span>
        ),
    },
    {
      key: 'status',
      header: t('common.status'),
      render: (r) => <RunStatusBadge status={r.status} />,
    },
    { key: 'step', header: t('automations.runs.step'), render: (r) => stepLabel(r, workflow) },
    {
      key: 'next',
      header: t('automations.runs.nextRun'),
      render: (r) => (r.finished_at ? t('common.dash') : formatDateTime(r.next_run_at)),
    },
    {
      key: 'attempts',
      header: t('automations.runs.attempts'),
      align: 'right',
      render: (r) => format.format(r.attempts),
    },
    {
      key: 'error',
      header: t('automations.runs.error'),
      render: (r) => {
        const reason = describeRunError(r.error_code, r.last_error);
        return reason ? (
          <span className="cf-text-sm cf-break">{reason.text}</span>
        ) : (
          t('common.dash')
        );
      },
    },
    { key: 'updated', header: t('common.updatedAt'), render: (r) => formatDateTime(r.updated_at) },
  ];

  return (
    <Card
      flush
      title={t('automations.runs.title')}
      description={t('automations.runs.description')}
      actions={
        <Button variant="ghost" icon={<IconRefresh size={16} />} onClick={runs.reload}>
          {t('common.refresh')}
        </Button>
      }
    >
      <div className="cf-toolbar">
        <div className="cf-field">
          <label className="cf-field__label" htmlFor="workflow-runs-status">
            {t('common.status')}
          </label>
          <Select
            id="workflow-runs-status"
            placeholder={t('common.all')}
            options={meta.run_statuses.map((s) => ({
              value: s,
              label: tEnum('automations.runStatus', s),
            }))}
            value={status}
            onChange={(e) => {
              setStatus(e.target.value as RunStatus | '');
              pager.reset();
            }}
          />
        </div>
      </div>
      <DataTable
        columns={columns}
        rows={runs.data?.items ?? []}
        rowKey={(r) => r.id}
        loading={runs.loading}
        error={runs.error}
        onRetry={runs.reload}
        empty={{ title: t('automations.runs.empty'), description: t('automations.runs.emptyHint') }}
        onRowClick={(r) => navigate(paths.automationRun(r.id))}
        pagination={{
          page: runs.data?.page ?? pager.page,
          perPage: pager.perPage,
          total: runs.data?.total ?? 0,
          totalPages: runs.data?.totalPages ?? 0,
          onPageChange: pager.setPage,
        }}
      />
    </Card>
  );
}
