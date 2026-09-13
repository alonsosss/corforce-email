import { useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { schedulerApi, schedulerHandlers, type SchedulerJob } from '@/api/scheduler';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import {
  Alert,
  Badge,
  Button,
  Card,
  DataTable,
  Select,
  useToast,
  type Column,
} from '@/design/components';
import { IconPlus, IconRefresh } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { localPage } from '@/lib/localPage';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { ActiveBadge } from '@/pages/shared/StatusBadges';
import { JobForm } from './JobForm';
import { isPlatformJob, tenantHandlers } from './jobDraft';
import { JobTypeBadge, ScheduleLabel } from './schedulerFormat';

/** Valor del filtro is_active tal como viaja en la query; vacio es sin filtro. */
type ActiveFilter = '' | 'true' | 'false';

export function JobsTab() {
  const navigate = useNavigate();
  const toast = useToast();
  const { can } = useAccess();
  const pager = usePagination();
  const [active, setActive] = useState<ActiveFilter>('');
  const [creating, setCreating] = useState(false);
  const canCreate = can(...PERMISSIONS.schedulerJobs.create);

  const jobs = useQuery(
    () => schedulerApi.listJobs({ is_active: active === '' ? undefined : active === 'true' }),
    [active],
  );
  // Solo quien puede crear necesita saber si hay manejadores a los que apuntar.
  const catalog = useQuery(async () => (canCreate ? schedulerHandlers.get() : null), [canCreate]);
  const catalogEmpty = catalog.data !== null && tenantHandlers(catalog.data).length === 0;
  // GET /scheduler/jobs devuelve la lista completa: se pagina en el cliente.
  const page = localPage(jobs.data ?? [], pager.page, pager.perPage);

  const columns: Column<SchedulerJob>[] = [
    {
      key: 'job',
      header: t('scheduler.column.job'),
      render: (job) => (
        <div className="cf-cell-stack">
          <Link to={paths.schedulerJob(job.id)} onClick={(e) => e.stopPropagation()}>
            <strong>{job.name}</strong>
          </Link>
          <span className="cf-mono cf-text-muted cf-text-sm">{job.code}</span>
        </div>
      ),
    },
    {
      key: 'type',
      header: t('scheduler.column.type'),
      render: (job) => <JobTypeBadge type={job.job_type} />,
    },
    {
      key: 'schedule',
      header: t('scheduler.column.schedule'),
      render: (job) => <ScheduleLabel job={job} />,
    },
    {
      key: 'handler',
      header: t('scheduler.column.handler'),
      render: (job) => <span className="cf-mono cf-text-sm cf-break">{job.handler}</span>,
    },
    {
      key: 'status',
      header: t('common.status'),
      render: (job) => (
        <span className="cf-inline-list">
          <ActiveBadge active={job.is_active} />
          {isPlatformJob(job) ? <Badge tone="accent">{t('scheduler.platform')}</Badge> : null}
        </span>
      ),
    },
    {
      key: 'updated',
      header: t('common.updatedAt'),
      render: (job) => formatDateTime(job.updated_at),
    },
  ];

  return (
    <div className="cf-stack">
      {catalogEmpty ? (
        <Alert tone="warning" title={t('scheduler.catalogEmpty.title')}>
          {t('scheduler.catalogEmpty.body')}
        </Alert>
      ) : null}
      <Card
        flush
        title={t('scheduler.jobs.title')}
        actions={
          <>
            <Button variant="ghost" icon={<IconRefresh size={16} />} onClick={jobs.reload}>
              {t('common.refresh')}
            </Button>
            {canCreate ? (
              <Button
                variant="primary"
                icon={<IconPlus size={16} />}
                onClick={() => setCreating(true)}
              >
                {t('scheduler.new')}
              </Button>
            ) : null}
          </>
        }
      >
        <div className="cf-toolbar">
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="scheduler-active">
              {t('common.status')}
            </label>
            <Select
              id="scheduler-active"
              placeholder={t('common.all')}
              options={[
                { value: 'true', label: t('common.active') },
                { value: 'false', label: t('common.inactive') },
              ]}
              value={active}
              onChange={(e) => {
                setActive(e.target.value as ActiveFilter);
                pager.reset();
              }}
            />
          </div>
        </div>
        <DataTable
          columns={columns}
          rows={page.items}
          rowKey={(job) => job.id}
          loading={jobs.loading}
          error={jobs.error}
          onRetry={jobs.reload}
          empty={{ title: t('scheduler.empty'), description: t('scheduler.emptyDescription') }}
          onRowClick={(job) => navigate(paths.schedulerJob(job.id))}
          pagination={{
            page: page.page,
            perPage: page.perPage,
            total: page.total,
            totalPages: page.totalPages,
            onPageChange: pager.setPage,
          }}
        />
      </Card>
      {creating ? (
        <JobForm
          job={null}
          onClose={() => setCreating(false)}
          onSaved={(job) => {
            toast.success(t('scheduler.created'));
            setCreating(false);
            navigate(paths.schedulerJob(job.id));
          }}
        />
      ) : null}
    </div>
  );
}
