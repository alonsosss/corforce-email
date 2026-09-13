import { useState } from 'react';
import { schedulerApi, schedulerMeta, type ScheduledTask } from '@/api/scheduler';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import {
  Badge,
  Button,
  Card,
  ConfirmDialog,
  DataTable,
  useToast,
  type Column,
} from '@/design/components';
import { IconRefresh } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { localPage } from '@/lib/localPage';
import { t, tEnum } from '@/i18n';
import { formatSeconds } from './schedulerFormat';

/** Tareas puntuales pendientes de la empresa (GET /scheduler/tasks, sin paginar). */
export function TasksTab() {
  const toast = useToast();
  const { can } = useAccess();
  const pager = usePagination();
  const [cancelling, setCancelling] = useState<ScheduledTask | null>(null);
  const canCancel = can(...PERMISSIONS.schedulerTasks.cancel);
  const tasks = useQuery(() => schedulerApi.listPendingTasks(), []);
  const page = localPage(tasks.data ?? [], pager.page, pager.perPage);
  // La ventana del listado la publica GET /scheduler/meta, que exige jobs/read. Sin ella, o si
  // la meta no carga, se describe el listado sin plazo.
  const canReadMeta = can(...PERMISSIONS.schedulerJobs.read);
  const meta = useQuery(async () => (canReadMeta ? schedulerMeta.get() : null), [canReadMeta]);
  const windowSeconds = meta.data?.tasks.pending_window_seconds;
  const windowLabel = windowSeconds ? formatSeconds(windowSeconds) : null;

  const columns: Column<ScheduledTask>[] = [
    {
      key: 'name',
      header: t('common.name'),
      render: (task) => (
        <div className="cf-cell-stack">
          <strong>{task.name}</strong>
          {task.description ? (
            <span className="cf-text-muted cf-text-sm">{task.description}</span>
          ) : null}
        </div>
      ),
    },
    {
      key: 'handler',
      header: t('scheduler.column.handler'),
      render: (task) => <span className="cf-mono cf-text-sm cf-break">{task.handler}</span>,
    },
    {
      key: 'trigger',
      header: t('scheduler.tasks.triggerAt'),
      render: (task) => formatDateTime(task.trigger_at),
    },
    {
      key: 'status',
      header: t('common.status'),
      render: (task) => <Badge tone="info">{tEnum('scheduler.taskStatus', task.status)}</Badge>,
    },
    {
      key: 'created',
      header: t('common.createdAt'),
      render: (task) => formatDateTime(task.created_at),
    },
    ...(canCancel
      ? [
          {
            key: 'actions',
            header: <span className="cf-visually-hidden">{t('common.actions')}</span>,
            align: 'right' as const,
            render: (task: ScheduledTask) => (
              <Button
                size="sm"
                variant="ghost"
                aria-label={t('scheduler.tasks.cancelLabel', { name: task.name })}
                onClick={() => setCancelling(task)}
              >
                {t('scheduler.executions.cancel')}
              </Button>
            ),
          },
        ]
      : []),
  ];

  return (
    <Card
      flush
      title={t('scheduler.tasks.title')}
      description={
        windowLabel
          ? t('scheduler.tasks.descriptionWindow', { window: windowLabel })
          : t('scheduler.tasks.description')
      }
      actions={
        <Button variant="ghost" icon={<IconRefresh size={16} />} onClick={tasks.reload}>
          {t('common.refresh')}
        </Button>
      }
    >
      <DataTable
        columns={columns}
        rows={page.items}
        rowKey={(task) => task.id}
        loading={tasks.loading}
        error={tasks.error}
        onRetry={tasks.reload}
        empty={{
          title: t('scheduler.tasks.empty'),
          description: windowLabel
            ? t('scheduler.tasks.emptyWindow', { window: windowLabel })
            : undefined,
        }}
        pagination={{
          page: page.page,
          perPage: page.perPage,
          total: page.total,
          totalPages: page.totalPages,
          onPageChange: pager.setPage,
        }}
      />
      <ConfirmDialog
        open={cancelling !== null}
        title={t('scheduler.tasks.cancel')}
        message={cancelling ? t('scheduler.tasks.cancelConfirm', { name: cancelling.name }) : ''}
        confirmLabel={t('scheduler.tasks.cancel')}
        danger
        onCancel={() => setCancelling(null)}
        onConfirm={async () => {
          if (!cancelling) return;
          await schedulerApi.cancelTask(cancelling.id);
          toast.success(t('scheduler.tasks.cancelled'));
          setCancelling(null);
          tasks.reload();
        }}
      />
    </Card>
  );
}
