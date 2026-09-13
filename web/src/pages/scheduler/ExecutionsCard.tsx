import { useState } from 'react';
import { ERROR_CODES } from '@/api/errors';
import { schedulerApi, type JobExecution, type SchedulerJob } from '@/api/scheduler';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import {
  Button,
  Card,
  ConfirmDialog,
  DataTable,
  EmptyState,
  useToast,
  type Column,
} from '@/design/components';
import { IconLock } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { getLocale, t, tEnum } from '@/i18n';
import { isPlatformJob } from './jobDraft';
import { ExecutionStatusBadge, formatDurationMs } from './schedulerFormat';

interface PendingAction {
  kind: 'cancel' | 'retry';
  execution: JobExecution;
}

// El mismo criterio que domain/lifecycle.go: solo se cancela una ejecucion pendiente o en
// curso y solo se reintenta una fallida. El servicio lo vuelve a decidir (409).
const isOpen = (e: JobExecution) => e.status === 'pending' || e.status === 'running';

function StartCell({ execution: e }: { execution: JobExecution }) {
  if (e.started_at) {
    return (
      <span className="cf-cell-stack">
        <span>{formatDateTime(e.started_at)}</span>
        {e.status === 'running' && e.deadline_at ? (
          <span className="cf-text-muted cf-text-sm">
            {t('scheduler.executions.deadline', { date: formatDateTime(e.deadline_at) })}
          </span>
        ) : null}
      </span>
    );
  }
  if (e.next_attempt_at) {
    return (
      <span className="cf-text-muted">
        {t('scheduler.executions.scheduledFor', { date: formatDateTime(e.next_attempt_at) })}
      </span>
    );
  }
  return <>{t('common.dash')}</>;
}

function ReasonCell({ execution: e }: { execution: JobExecution }) {
  if (!e.failure_reason && !e.error_message) return <>{t('common.dash')}</>;
  return (
    <span className="cf-cell-stack">
      {e.failure_reason ? <span>{tEnum('scheduler.failureReason', e.failure_reason)}</span> : null}
      {e.error_message ? (
        <span className="cf-text-muted cf-text-sm cf-break">{e.error_message}</span>
      ) : null}
    </span>
  );
}

interface ExecutionsCardProps {
  job: SchedulerJob;
  refreshKey: number;
  /** Tras cancelar o reintentar: la ultima ejecucion del trabajo puede haber cambiado. */
  onChange: () => void;
}

/** Historial paginado de un trabajo (GET /scheduler/jobs/{id}/history). */
export function ExecutionsCard({ job, refreshKey, onChange }: ExecutionsCardProps) {
  const toast = useToast();
  const { can } = useAccess();
  const pager = usePagination();
  const [pending, setPending] = useState<PendingAction | null>(null);
  const canRead = can(...PERMISSIONS.schedulerExecutions.read);
  const platform = isPlatformJob(job);
  const canCancel = !platform && can(...PERMISSIONS.schedulerExecutions.cancel);
  const canRetry = !platform && can(...PERMISSIONS.schedulerExecutions.retry);
  const history = useQuery(
    async () =>
      canRead ? schedulerApi.history(job.id, { page: pager.page, per_page: pager.perPage }) : null,
    [job.id, canRead, pager.page, pager.perPage, refreshKey],
  );

  if (!canRead) {
    return (
      <Card title={t('scheduler.executions.title')}>
        <EmptyState icon={<IconLock size={32} />} title={t('common.missingPermission')} />
      </Card>
    );
  }

  const number = new Intl.NumberFormat(getLocale());
  const attempt = (e: JobExecution) => number.format(e.retry_count + 1);
  const columns: Column<JobExecution>[] = [
    {
      key: 'status',
      header: t('common.status'),
      render: (e) => <ExecutionStatusBadge status={e.status} />,
    },
    {
      key: 'attempt',
      header: t('scheduler.executions.attempt'),
      align: 'right',
      render: attempt,
    },
    {
      key: 'start',
      header: t('scheduler.executions.start'),
      render: (e) => <StartCell execution={e} />,
    },
    {
      key: 'duration',
      header: t('scheduler.executions.duration'),
      align: 'right',
      render: (e) => formatDurationMs(e.duration_ms),
    },
    {
      key: 'reason',
      header: t('scheduler.executions.reason'),
      render: (e) => <ReasonCell execution={e} />,
    },
  ];
  if (canCancel || canRetry) {
    columns.push({
      key: 'actions',
      header: <span className="cf-visually-hidden">{t('common.actions')}</span>,
      align: 'right',
      render: (e) => {
        const vars = { n: attempt(e), date: formatDateTime(e.created_at) };
        return (
          <div className="cf-table__actions">
            {canCancel && isOpen(e) ? (
              <Button
                size="sm"
                variant="ghost"
                aria-label={t('scheduler.executions.cancelLabel', vars)}
                onClick={() => setPending({ kind: 'cancel', execution: e })}
              >
                {t('scheduler.executions.cancel')}
              </Button>
            ) : null}
            {canRetry && e.status === 'failed' ? (
              <Button
                size="sm"
                variant="ghost"
                aria-label={t('scheduler.executions.retryLabel', vars)}
                onClick={() => setPending({ kind: 'retry', execution: e })}
              >
                {t('scheduler.executions.retry')}
              </Button>
            ) : null}
          </div>
        );
      },
    });
  }

  const done = (message: string) => {
    toast.success(message);
    setPending(null);
    history.reload();
    onChange();
  };

  return (
    <Card
      flush
      title={t('scheduler.executions.title')}
      description={t('scheduler.executions.description')}
    >
      <DataTable
        columns={columns}
        rows={history.data?.items ?? []}
        rowKey={(e) => e.id}
        loading={history.loading}
        error={history.error}
        onRetry={history.reload}
        empty={{
          title: t('scheduler.executions.empty'),
          description: t('scheduler.executions.emptyHint'),
        }}
        pagination={{
          page: history.data?.page ?? pager.page,
          perPage: pager.perPage,
          total: history.data?.total ?? 0,
          totalPages: history.data?.totalPages ?? 0,
          onPageChange: pager.setPage,
        }}
      />
      <ConfirmDialog
        open={pending?.kind === 'cancel'}
        title={t('scheduler.executions.cancelTitle')}
        message={t('scheduler.executions.cancelConfirm')}
        confirmLabel={t('scheduler.executions.cancelTitle')}
        danger
        errorOverrides={{ [ERROR_CODES.CONFLICT]: 'scheduler.executions.cancelConflict' }}
        onCancel={() => setPending(null)}
        onConfirm={async () => {
          if (!pending) return;
          await schedulerApi.cancelExecution(pending.execution.id);
          done(t('scheduler.executions.cancelled'));
        }}
      />
      <ConfirmDialog
        open={pending?.kind === 'retry'}
        title={t('scheduler.executions.retryTitle')}
        message={t('scheduler.executions.retryConfirm')}
        confirmLabel={t('scheduler.executions.retryTitle')}
        errorOverrides={{
          [ERROR_CODES.CONFLICT]: 'scheduler.executions.retryConflict',
          [ERROR_CODES.VALIDATION_ERROR]: 'scheduler.error.handlerNotAllowed',
        }}
        onCancel={() => setPending(null)}
        onConfirm={async () => {
          if (!pending) return;
          await schedulerApi.retryExecution(pending.execution.id);
          done(t('scheduler.executions.retried'));
        }}
      />
    </Card>
  );
}
