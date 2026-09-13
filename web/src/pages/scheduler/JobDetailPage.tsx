import { useState } from 'react';
import { useParams } from 'react-router-dom';
import { ERROR_CODES } from '@/api/errors';
import { schedulerApi, schedulerHandlers } from '@/api/scheduler';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useQuery } from '@/hooks/useQuery';
import {
  Alert,
  Badge,
  Button,
  Card,
  ConfirmDialog,
  DescriptionList,
  ErrorState,
  PageHeader,
  Skeleton,
  useToast,
} from '@/design/components';
import { IconEdit, IconPause, IconPlay, IconRefresh } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { getLocale, t } from '@/i18n';
import { paths } from '@/paths';
import { MissingPermission } from '@/pages/shared/MissingPermission';
import { ActiveBadge } from '@/pages/shared/StatusBadges';
import { ExecutionsCard } from './ExecutionsCard';
import { JobForm } from './JobForm';
import { handlerOf, isPlatformJob } from './jobDraft';
import {
  formatSeconds,
  JobTypeBadge,
  LastExecutionLabel,
  NextRunLabel,
  prettyPayload,
  ScheduleLabel,
} from './schedulerFormat';

type Dialog = 'edit' | 'activate' | 'deactivate' | 'run' | null;

/** Detalle de un trabajo: su definicion, sus acciones y el historial de ejecuciones. */
export default function JobDetailPage() {
  const { id = '' } = useParams();
  const toast = useToast();
  const { can } = useAccess();
  const canRead = can(...PERMISSIONS.schedulerJobs.read);
  const [dialog, setDialog] = useState<Dialog>(null);
  // Cambia tras lanzar o refrescar: el historial vuelve a pedirse.
  const [historyKey, setHistoryKey] = useState(0);
  const job = useQuery(
    async () => (canRead ? (await schedulerApi.getJob(id)).data : null),
    [id, canRead],
  );
  const catalog = useQuery(async () => (canRead ? schedulerHandlers.get() : null), [canRead]);

  if (!canRead) return <MissingPermission title={t('scheduler.title')} />;
  const back = { to: paths.scheduler, label: t('nav.scheduler') };

  if (job.error) {
    return (
      <div>
        <PageHeader title={t('scheduler.title')} back={back} />
        <Card>
          <ErrorState error={job.error} title={t('scheduler.notFound')} onRetry={job.reload} />
        </Card>
      </div>
    );
  }
  if (!job.data) {
    return (
      <Card>
        <Skeleton lines={8} />
      </Card>
    );
  }

  const j = job.data;
  const platform = isPlatformJob(j);
  const canUpdate = !platform && can(...PERMISSIONS.schedulerJobs.update);
  const canRun = !platform && can(...PERMISSIONS.schedulerJobs.run);
  // already_run lo calcula el servicio con la regla con la que rechaza reactivar (409).
  const spent = !j.is_active && j.already_run;
  const handler = catalog.data ? handlerOf(catalog.data, j) : null;
  const outOfCatalog = catalog.data !== null && handler === null;
  const number = new Intl.NumberFormat(getLocale());
  const close = () => setDialog(null);

  const timeout =
    j.timeout_seconds === 0
      ? handler
        ? t('scheduler.detail.timeoutHandlerMax', {
            value: formatSeconds(handler.max_timeout_seconds),
          })
        : t('scheduler.detail.timeoutHandlerMaxUnknown')
      : handler && j.timeout_seconds > handler.max_timeout_seconds
        ? t('scheduler.detail.timeoutCapped', {
            value: formatSeconds(j.timeout_seconds),
            max: formatSeconds(handler.max_timeout_seconds),
          })
        : formatSeconds(j.timeout_seconds);

  const setActive = async (active: boolean) => {
    await (active ? schedulerApi.enableJob(j.id) : schedulerApi.disableJob(j.id));
    toast.success(active ? t('scheduler.activated') : t('scheduler.deactivated'));
    close();
    job.reload();
  };

  return (
    <div>
      <PageHeader
        title={j.name}
        description={
          <span className="cf-inline">
            <JobTypeBadge type={j.job_type} />
            <ActiveBadge active={j.is_active} />
            {platform ? <Badge tone="accent">{t('scheduler.platform')}</Badge> : null}
          </span>
        }
        back={back}
        actions={
          <>
            <Button
              variant="ghost"
              icon={<IconRefresh size={16} />}
              onClick={() => {
                job.reload();
                setHistoryKey((k) => k + 1);
              }}
            >
              {t('common.refresh')}
            </Button>
            {canRun ? (
              <Button icon={<IconPlay size={16} />} onClick={() => setDialog('run')}>
                {t('scheduler.run')}
              </Button>
            ) : null}
            {canUpdate ? (
              <Button icon={<IconEdit size={16} />} onClick={() => setDialog('edit')}>
                {t('common.edit')}
              </Button>
            ) : null}
            {canUpdate && j.is_active ? (
              <Button icon={<IconPause size={16} />} onClick={() => setDialog('deactivate')}>
                {t('common.deactivate')}
              </Button>
            ) : null}
            {canUpdate && !j.is_active && !spent ? (
              <Button icon={<IconPlay size={16} />} onClick={() => setDialog('activate')}>
                {t('common.activate')}
              </Button>
            ) : null}
          </>
        }
      />
      <div className="cf-stack">
        {j.description ? <p className="cf-text-secondary">{j.description}</p> : null}
        {platform ? <Alert tone="info">{t('scheduler.platformHint')}</Alert> : null}
        {canUpdate && spent ? <Alert tone="info">{t('scheduler.alreadyRunHint')}</Alert> : null}
        {outOfCatalog ? (
          <Alert tone="warning" title={t('scheduler.handler.outOfCatalog')}>
            {t('scheduler.handler.outOfCatalogHint')}
          </Alert>
        ) : null}
        <Card>
          <DescriptionList
            items={[
              {
                label: t('scheduler.detail.code'),
                value: <span className="cf-mono">{j.code}</span>,
              },
              { label: t('scheduler.column.schedule'), value: <ScheduleLabel job={j} /> },
              { label: t('scheduler.column.nextRun'), value: <NextRunLabel job={j} /> },
              {
                label: t('scheduler.detail.lastRun'),
                value: (
                  <span className="cf-cell-stack">
                    <span>{formatDateTime(j.last_run_at)}</span>
                    <span className="cf-text-muted cf-text-sm">
                      {t('scheduler.detail.lastRunHint')}
                    </span>
                  </span>
                ),
              },
              {
                label: t('scheduler.column.lastExecution'),
                value: <LastExecutionLabel execution={j.last_execution} detailed />,
              },
              {
                label: t('scheduler.column.handler'),
                value: (
                  <span className="cf-cell-stack">
                    <span className="cf-mono cf-break">{j.handler}</span>
                    {handler?.description ? (
                      <span className="cf-text-muted cf-text-sm">{handler.description}</span>
                    ) : null}
                  </span>
                ),
              },
              { label: t('scheduler.detail.maxRetries'), value: number.format(j.max_retries) },
              { label: t('scheduler.detail.timeout'), value: timeout },
              {
                label: t('scheduler.detail.payload'),
                value: j.payload ? (
                  <pre className="cf-pre">{prettyPayload(j.payload)}</pre>
                ) : (
                  t('common.dash')
                ),
              },
              { label: t('common.createdAt'), value: formatDateTime(j.created_at) },
              { label: t('common.updatedAt'), value: formatDateTime(j.updated_at) },
              { label: t('common.id'), value: <span className="cf-mono cf-break">{j.id}</span> },
            ]}
          />
        </Card>
        <ExecutionsCard job={j} refreshKey={historyKey} onChange={job.reload} />
      </div>

      {dialog === 'edit' ? (
        <JobForm
          job={j}
          onClose={close}
          onSaved={(saved) => {
            toast.success(t('scheduler.updated'));
            close();
            job.setData(saved);
          }}
        />
      ) : null}
      <ConfirmDialog
        open={dialog === 'run'}
        title={t('scheduler.run')}
        message={t('scheduler.runConfirm', { name: j.name })}
        confirmLabel={t('scheduler.run')}
        errorOverrides={{ [ERROR_CODES.VALIDATION_ERROR]: 'scheduler.error.handlerNotAllowed' }}
        onCancel={close}
        onConfirm={async () => {
          await schedulerApi.runJob(j.id);
          toast.success(t('scheduler.launched'));
          close();
          job.reload();
          setHistoryKey((k) => k + 1);
        }}
      />
      <ConfirmDialog
        open={dialog === 'activate'}
        title={t('common.activate')}
        message={t('scheduler.activateConfirm', { name: j.name })}
        confirmLabel={t('common.activate')}
        errorOverrides={{
          [ERROR_CODES.VALIDATION_ERROR]: 'scheduler.error.invalidSchedule',
          [ERROR_CODES.INVALID_TIMEZONE]: 'scheduler.error.invalidSchedule',
        }}
        onCancel={close}
        onConfirm={() => setActive(true)}
      />
      <ConfirmDialog
        open={dialog === 'deactivate'}
        title={t('common.deactivate')}
        message={t('scheduler.deactivateConfirm', { name: j.name })}
        confirmLabel={t('common.deactivate')}
        danger
        onCancel={close}
        onConfirm={() => setActive(false)}
      />
    </div>
  );
}
