import { useState } from 'react';
import type { MigrationFolderProgress, MigrationJob } from '@/api/mailMigration';
import {
  Alert,
  Badge,
  Button,
  Card,
  ConfirmDialog,
  DataTable,
  DescriptionList,
  Meter,
  type Column,
} from '@/design/components';
import { IconStop } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { formatBytes } from '@/lib/quota';
import { getLocale, t, tEnum } from '@/i18n';
import {
  failureMessage,
  isActiveJob,
  messagesProcessed,
  MIGRATION_ERRORS,
  progressPercent,
  progressRatio,
  statusTone,
} from './migration';

export interface MigrationJobCardProps {
  job: MigrationJob;
  canCancel: boolean;
  onCancel: (job: MigrationJob) => Promise<void>;
}

function progressSummary(job: MigrationJob, format: Intl.NumberFormat): string {
  const p = job.progress;
  const percent = progressPercent(job);
  if (p.messages_total > 0) {
    return t('migration.progress.messages', {
      done: format.format(messagesProcessed(job)),
      total: format.format(p.messages_total),
      percent: percent ?? 0,
    });
  }
  if (p.folders_total > 0) {
    return t('migration.progress.folders', {
      done: format.format(p.folders_done),
      total: format.format(p.folders_total),
    });
  }
  return t('migration.progress.preparing');
}

export function MigrationJobCard({ job, canCancel, onCancel }: MigrationJobCardProps) {
  const [cancelling, setCancelling] = useState(false);
  const format = new Intl.NumberFormat(getLocale());
  const active = isActiveJob(job);
  const p = job.progress;

  const folderColumns: Column<MigrationFolderProgress>[] = [
    {
      key: 'name',
      header: t('migration.folder.name'),
      render: (f) => <span className="cf-break">{f.name}</span>,
    },
    {
      key: 'copied',
      header: t('migration.folder.copied'),
      align: 'right',
      render: (f) => format.format(f.messages_copied),
    },
    {
      key: 'skipped',
      header: t('migration.folder.skipped'),
      align: 'right',
      render: (f) => format.format(f.messages_skipped),
    },
    {
      key: 'failed',
      header: t('migration.folder.failed'),
      align: 'right',
      render: (f) => format.format(f.messages_failed),
    },
  ];

  return (
    <Card
      title={t('migration.job.title')}
      description={`${job.source_username} @ ${job.source_host}:${job.source_port}`}
      actions={
        canCancel && active && !job.cancel_requested_at ? (
          <Button
            variant="danger"
            icon={<IconStop size={16} />}
            onClick={() => setCancelling(true)}
          >
            {t('migration.job.cancel')}
          </Button>
        ) : null
      }
    >
      <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
        <div
          role="status"
          aria-live="polite"
          aria-atomic="true"
          className="cf-stack"
          style={{ gap: 'var(--cf-space-2)' }}
        >
          <span className="cf-inline">
            <Badge tone={statusTone(job.status)}>{tEnum('migration.status', job.status)}</Badge>
            {job.phase ? (
              <span className="cf-text-secondary">{tEnum('migration.phase', job.phase)}</span>
            ) : null}
            {active && job.cancel_requested_at ? (
              <Badge tone="warning">{t('migration.job.cancelRequested')}</Badge>
            ) : null}
          </span>
          <span>{progressSummary(job, format)}</span>
        </div>
        <Meter ratio={progressRatio(job) ?? 0} label={t('migration.progress.label')} />

        {job.last_error ? (
          <Alert tone="danger" title={t('migration.job.lastError')}>
            <div>{failureMessage(job.last_error.code)}</div>
            {job.last_error.message ? (
              <div className="cf-text-sm cf-break">{job.last_error.message}</div>
            ) : null}
          </Alert>
        ) : null}

        <DescriptionList
          items={[
            {
              label: t('migration.job.folders'),
              value: t('migration.job.foldersValue', {
                done: format.format(p.folders_done),
                total: format.format(p.folders_total),
              }),
            },
            { label: t('migration.job.copied'), value: format.format(p.messages_copied) },
            { label: t('migration.job.skipped'), value: format.format(p.messages_skipped) },
            { label: t('migration.job.failed'), value: format.format(p.messages_failed) },
            { label: t('migration.job.bytes'), value: formatBytes(p.bytes_copied) },
            { label: t('migration.job.attempt'), value: format.format(job.attempt) },
            { label: t('migration.job.createdAt'), value: formatDateTime(job.created_at) },
            { label: t('migration.job.startedAt'), value: formatDateTime(job.started_at) },
            { label: t('migration.job.heartbeatAt'), value: formatDateTime(job.heartbeat_at) },
            { label: t('migration.job.finishedAt'), value: formatDateTime(job.finished_at) },
          ]}
        />

        {p.folders.length > 0 ? (
          <DataTable columns={folderColumns} rows={p.folders} rowKey={(f) => f.name} />
        ) : null}
      </div>
      <ConfirmDialog
        open={cancelling}
        title={t('migration.job.cancel')}
        message={t('migration.job.cancelConfirm')}
        confirmLabel={t('migration.job.cancelAction')}
        danger
        errorOverrides={MIGRATION_ERRORS}
        onCancel={() => setCancelling(false)}
        onConfirm={async () => {
          await onCancel(job);
          setCancelling(false);
        }}
      />
    </Card>
  );
}
