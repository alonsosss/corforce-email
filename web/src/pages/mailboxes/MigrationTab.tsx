import { useEffect, useRef, useState } from 'react';
import type { Mailbox } from '@/api/mailDirectory';
import { errorCode } from '@/api/errors';
import {
  mailMigrationApi,
  type CreateMigrationRequest,
  type MigrationJob,
  type MigrationMeta,
} from '@/api/mailMigration';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useQuery } from '@/hooks/useQuery';
import {
  Alert,
  Badge,
  Button,
  Card,
  DataTable,
  ErrorState,
  Skeleton,
  useToast,
  type Column,
} from '@/design/components';
import { IconRefresh } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { t, tEnum } from '@/i18n';
import { ResourceGate } from '@/pages/shared/ResourceGate';
import { findActiveJob, launchBlock, shouldPoll, statusTone } from './migration';
import { MigrationForm } from './MigrationForm';
import { MigrationJobCard } from './MigrationJobCard';

// Cada cuanto se vuelve a leer la lista mientras haya un trabajo pendiente o en curso.
export const POLL_INTERVAL_MS = 4000;
// Cuantos trabajos recientes del buzon se listan.
const JOBS_LIMIT = 10;
const STALE_STATE_ERRORS = ['JOB_ALREADY_ACTIVE', 'TENANT_LIMIT_REACHED'];

/** Migracion de un buzon desde otro servidor IMAP: lanzar, seguir el avance y cancelar. */
export function MigrationTab({ mailbox }: { mailbox: Mailbox }) {
  const meta = useQuery(() => mailMigrationApi.meta(), []);
  return (
    <ResourceGate resource={meta}>
      {(m) => <MigrationBody mailbox={mailbox} meta={m} onMetaStale={meta.reload} />}
    </ResourceGate>
  );
}

interface MigrationBodyProps {
  mailbox: Mailbox;
  meta: MigrationMeta;
  onMetaStale: () => void;
}

function MigrationBody({ mailbox, meta, onMetaStale }: MigrationBodyProps) {
  const { can } = useAccess();
  const toast = useToast();
  const jobs = useQuery(
    async () => (await mailMigrationApi.listJobs(mailbox.id, JOBS_LIMIT)).items,
    [mailbox.id],
  );
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const { reload, setData } = jobs;

  const polling = shouldPoll(jobs.data, Boolean(jobs.error));
  useEffect(() => {
    if (!polling) return;
    const timer = setTimeout(reload, POLL_INTERVAL_MS);
    return () => clearTimeout(timer);
  }, [polling, jobs.data, reload]);

  // Al terminar un trabajo cambia el numero de trabajos activos de la empresa.
  const hasActive = jobs.data ? findActiveJob(jobs.data) !== null : null;
  const wasActive = useRef<boolean | null>(null);
  useEffect(() => {
    if (wasActive.current === true && hasActive === false) onMetaStale();
    wasActive.current = hasActive;
  }, [hasActive, onMetaStale]);

  const create = async (request: CreateMigrationRequest) => {
    let job: MigrationJob;
    try {
      job = await mailMigrationApi.createJob(request);
    } catch (err) {
      // La pantalla se quedo atras (otra pestana, otro administrador): se vuelve a leer.
      if (STALE_STATE_ERRORS.includes(errorCode(err))) {
        reload();
        onMetaStale();
      }
      throw err;
    }
    setSelectedId(null);
    setData((current) => [job, ...(current ?? []).filter((j) => j.id !== job.id)]);
    toast.success(t('migration.form.started'));
  };

  const cancel = async (job: MigrationJob) => {
    const updated = await mailMigrationApi.cancelJob(job.id);
    setData((current) => current?.map((j) => (j.id === updated.id ? updated : j)) ?? current);
    toast.success(t('migration.job.cancelled'));
  };

  if (jobs.error && !jobs.data) {
    return (
      <Card title={t('migration.title')}>
        <ErrorState error={jobs.error} onRetry={reload} />
      </Card>
    );
  }
  if (!jobs.data) {
    return (
      <Card title={t('migration.title')}>
        <Skeleton lines={6} />
      </Card>
    );
  }

  const list = jobs.data;
  const shown = list.find((j) => j.id === selectedId) ?? findActiveJob(list) ?? list[0] ?? null;
  const block = launchBlock(meta, list);

  const columns: Column<MigrationJob>[] = [
    {
      key: 'status',
      header: t('migration.column.status'),
      render: (j) => (
        <Badge tone={statusTone(j.status)}>{tEnum('migration.status', j.status)}</Badge>
      ),
    },
    {
      key: 'source',
      header: t('migration.column.source'),
      render: (j) => <span className="cf-break">{`${j.source_host}:${j.source_port}`}</span>,
    },
    {
      key: 'createdAt',
      header: t('migration.column.createdAt'),
      render: (j) => formatDateTime(j.created_at),
    },
    {
      key: 'finishedAt',
      header: t('migration.column.finishedAt'),
      render: (j) => formatDateTime(j.finished_at),
    },
    {
      key: 'actions',
      header: '',
      align: 'right',
      render: (j) =>
        j.id === shown?.id ? null : (
          <Button size="sm" onClick={() => setSelectedId(j.id)}>
            {t('migration.column.view')}
          </Button>
        ),
    },
  ];

  return (
    <div className="cf-stack">
      {!meta.configured ? (
        <Alert tone="warning" title={t('migration.notConfigured.title')}>
          {t('migration.notConfigured.body')}
        </Alert>
      ) : null}
      {jobs.error ? (
        <Alert tone="danger" title={t('migration.refreshFailed')}>
          <Button size="sm" icon={<IconRefresh size={14} />} onClick={reload}>
            {t('common.retry')}
          </Button>
        </Alert>
      ) : null}
      {shown ? (
        <MigrationJobCard
          job={shown}
          canCancel={can(...PERMISSIONS.mailMigrationJobs.cancel)}
          onCancel={cancel}
        />
      ) : null}
      {block === 'jobActive' ? <Alert tone="info">{t('migration.block.jobActive')}</Alert> : null}
      {block === 'tenantLimit' ? (
        <Alert tone="warning">
          {t('migration.block.tenantLimit', { max: meta.max_active_jobs })}
        </Alert>
      ) : null}
      {block === null && can(...PERMISSIONS.mailMigrationJobs.create) ? (
        <MigrationForm mailboxId={mailbox.id} meta={meta} onSubmit={create} />
      ) : null}
      {list.length > 1 ? (
        <Card
          flush
          title={t('migration.history.title')}
          description={t('migration.history.description', { n: JOBS_LIMIT })}
        >
          <DataTable columns={columns} rows={list} rowKey={(j) => j.id} />
        </Card>
      ) : null}
    </div>
  );
}
