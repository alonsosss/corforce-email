import { useState } from 'react';
import { mailSecurityApi, QUEUE_MAX_LIMIT, type QueueMessage } from '@/api/mailSecurity';
import { isApiError, ERROR_CODES } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import {
  Alert,
  Badge,
  Button,
  Card,
  ConfirmDialog,
  DataTable,
  PageHeader,
  useToast,
  type BadgeTone,
  type Column,
} from '@/design/components';
import { IconRefresh } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { formatBytes } from '@/lib/quota';
import { t, type MessageKey } from '@/i18n';

const STATE_LABELS: Partial<Record<string, MessageKey>> = {
  incoming: 'mailQueue.state.incoming',
  active: 'mailQueue.state.active',
  deferred: 'mailQueue.state.deferred',
  hold: 'mailQueue.state.hold',
  corrupt: 'mailQueue.state.corrupt',
};

const STATE_TONES: Record<string, BadgeTone> = {
  active: 'success',
  deferred: 'warning',
  hold: 'info',
  corrupt: 'danger',
};

function stateLabel(name: string): string {
  const key = STATE_LABELS[name];
  return key ? t(key) : name;
}

/** Cola de correo de la celda: solo el superadmin (permiso de plataforma mail_security/queue). */
export default function MailQueuePage() {
  const toast = useToast();
  const queue = useQuery(() => mailSecurityApi.listQueue(QUEUE_MAX_LIMIT), []);
  const [deleting, setDeleting] = useState<QueueMessage | null>(null);
  const [flushing, setFlushing] = useState(false);

  const act = useAction(async (message: QueueMessage, action: 'retry' | 'hold' | 'unhold') => {
    await mailSecurityApi.queueAction(message.queue_id, action);
  });

  const run = async (message: QueueMessage, action: 'retry' | 'hold' | 'unhold') => {
    if (await act.run(message, action)) {
      toast.success(t(`mailQueue.${action}Done`, { id: message.queue_id }));
      queue.reload();
      return;
    }
  };

  const notConfigured = isApiError(queue.error) && queue.error.code === ERROR_CODES.NOT_CONFIGURED;

  const columns: Column<QueueMessage>[] = [
    {
      key: 'arrived',
      header: t('mailQueue.column.arrived'),
      render: (m) => formatDateTime(new Date(m.arrival_time * 1000).toISOString()),
    },
    {
      key: 'state',
      header: t('mailQueue.column.state'),
      render: (m) => (
        <Badge tone={STATE_TONES[m.queue_name] ?? 'neutral'}>{stateLabel(m.queue_name)}</Badge>
      ),
    },
    {
      key: 'sender',
      header: t('mailQueue.column.sender'),
      render: (m) =>
        m.sender ? <span className="cf-mono cf-break">{m.sender}</span> : t('mailQueue.bounce'),
    },
    {
      key: 'recipients',
      header: t('mailQueue.column.recipients'),
      render: (m) => (
        <span className="cf-mono cf-break">
          {m.recipients[0]?.address ?? ''}
          {m.recipients_total > 1
            ? ` ${t('mailQueue.moreRecipients', { n: m.recipients_total - 1 })}`
            : ''}
        </span>
      ),
    },
    {
      key: 'reason',
      header: t('mailQueue.column.reason'),
      render: (m) => (
        <span className="cf-break">
          {m.recipients.find((r) => r.delay_reason)?.delay_reason ?? ''}
        </span>
      ),
    },
    {
      key: 'size',
      header: t('mailQueue.column.size'),
      align: 'right',
      render: (m) => formatBytes(m.message_size),
    },
    {
      key: 'actions',
      header: t('common.actions'),
      render: (m) => (
        <div
          role="group"
          aria-label={t('mailQueue.actionsFor', { id: m.queue_id })}
          className="cf-row-actions"
        >
          <Button size="sm" disabled={act.busy} onClick={() => void run(m, 'retry')}>
            {t('mailQueue.retry')}
          </Button>
          {m.queue_name === 'hold' ? (
            <Button size="sm" disabled={act.busy} onClick={() => void run(m, 'unhold')}>
              {t('mailQueue.unhold')}
            </Button>
          ) : (
            <Button size="sm" disabled={act.busy} onClick={() => void run(m, 'hold')}>
              {t('mailQueue.hold')}
            </Button>
          )}
          <Button size="sm" variant="danger" disabled={act.busy} onClick={() => setDeleting(m)}>
            {t('mailQueue.delete')}
          </Button>
        </div>
      ),
    },
  ];

  const data = queue.data;
  return (
    <div>
      <PageHeader
        title={t('mailQueue.title')}
        description={t('mailQueue.subtitle')}
        actions={
          <>
            <Button onClick={queue.reload} disabled={queue.loading}>
              <IconRefresh size={16} />
              {t('common.refresh')}
            </Button>
            <Button onClick={() => setFlushing(true)} disabled={notConfigured}>
              {t('mailQueue.flush')}
            </Button>
          </>
        }
      />
      {act.error ? (
        <Alert tone="danger">
          {isApiError(act.error) && act.error.status === 404
            ? t('mailQueue.gone')
            : errorMessage(act.error)}
        </Alert>
      ) : null}
      {notConfigured ? (
        <Alert tone="warning">{t('mailQueue.notConfigured')}</Alert>
      ) : (
        <Card flush>
          <DataTable
            columns={columns}
            rows={data?.items ?? []}
            rowKey={(m) => m.queue_id}
            loading={queue.loading}
            error={queue.error}
            onRetry={queue.reload}
            empty={{ title: t('mailQueue.empty') }}
          />
          {data && data.truncated ? (
            <p className="cf-text-sm cf-text-secondary">
              {t('mailQueue.showing', { shown: data.items.length, total: data.total })}
            </p>
          ) : null}
        </Card>
      )}
      <ConfirmDialog
        open={deleting !== null}
        title={t('mailQueue.deleteTitle')}
        message={
          deleting
            ? t('mailQueue.deleteConfirm', {
                id: deleting.queue_id,
                sender: deleting.sender || t('mailQueue.bounce'),
              })
            : ''
        }
        confirmLabel={t('mailQueue.delete')}
        danger
        onConfirm={async () => {
          if (!deleting) return;
          await mailSecurityApi.deleteQueueMessage(deleting.queue_id);
          toast.success(t('mailQueue.deleteDone', { id: deleting.queue_id }));
          setDeleting(null);
          queue.reload();
        }}
        onCancel={() => setDeleting(null)}
      />
      <ConfirmDialog
        open={flushing}
        title={t('mailQueue.flushTitle')}
        message={t('mailQueue.flushConfirm')}
        confirmLabel={t('mailQueue.flush')}
        onConfirm={async () => {
          await mailSecurityApi.flushQueue();
          toast.success(t('mailQueue.flushDone'));
          setFlushing(false);
          queue.reload();
        }}
        onCancel={() => setFlushing(false)}
      />
    </div>
  );
}
