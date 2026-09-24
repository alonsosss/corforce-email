import { useState } from 'react';
import { ERROR_CODES, errorCode } from '@/api/errors';
import { webmailApi, type ScheduledSend } from '@/api/webmail';
import { useQuery } from '@/hooks/useQuery';
import {
  Badge,
  Button,
  Card,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  PageHeader,
  Skeleton,
  useToast,
} from '@/design/components';
import { IconClock, IconX } from '@/design/icons';
import { t, tEnum } from '@/i18n';
import { paths } from '@/paths';
import { formatScheduled } from './schedule';
import { ScheduleDialog } from './ScheduleDialog';
import { useWebmailOutlet } from './webmailContext';

/** Envios programados del buzon: cambiar la hora o cancelar (el mensaje vuelve a Borradores). */
export default function ScheduledPage() {
  const toast = useToast();
  const { reloadFolders } = useWebmailOutlet();
  const scheduled = useQuery((signal) => webmailApi.scheduled(signal), []);
  const [rescheduling, setRescheduling] = useState<ScheduledSend | null>(null);
  const [canceling, setCanceling] = useState<ScheduledSend | null>(null);
  const items = scheduled.data ?? [];

  // Un envio que ya salio o esta saliendo no se toca: se muestra el error y se relee la lista.
  const guarded = async (action: () => Promise<void>) => {
    try {
      await action();
    } catch (err) {
      const code = errorCode(err);
      if (
        code === ERROR_CODES.SCHEDULED_SEND_NOT_PENDING ||
        code === ERROR_CODES.SCHEDULED_SEND_NOT_FOUND
      ) {
        scheduled.reload();
      }
      throw err;
    }
  };

  return (
    <div className="cf-wm-page">
      <PageHeader
        title={t('webmail.scheduled.title')}
        description={t('webmail.scheduled.description')}
        back={{ to: paths.webmail, label: t('webmail.settings.back') }}
      />
      <Card flush>
        {scheduled.error && !scheduled.data ? (
          <ErrorState error={scheduled.error} onRetry={scheduled.reload} />
        ) : !scheduled.data ? (
          <div className="cf-wm-pad">
            <Skeleton lines={4} />
          </div>
        ) : items.length === 0 ? (
          <EmptyState
            icon={<IconClock size={32} />}
            title={t('webmail.scheduled.empty')}
            description={t('webmail.scheduled.emptyHint')}
          />
        ) : (
          <ul className="cf-wm-scheduled">
            {items.map((item) => (
              <li key={item.id} className="cf-wm-scheduled__item">
                <div className="cf-wm-scheduled__main">
                  <span className="cf-wm-scheduled__subject">
                    {item.subject || t('webmail.noSubject')}
                  </span>
                  <span className="cf-text-sm cf-text-secondary cf-truncate">
                    {item.recipients.join(', ') || t('webmail.list.noRecipients')}
                  </span>
                </div>
                <span className="cf-wm-scheduled__when">
                  <IconClock size={14} />
                  {formatScheduled(item.send_at)}
                </span>
                <Badge tone="info">{tEnum('webmail.scheduled.status', item.status)}</Badge>
                <div className="cf-wm-scheduled__actions">
                  <Button size="sm" onClick={() => setRescheduling(item)}>
                    {t('webmail.scheduled.reschedule')}
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    icon={<IconX size={16} />}
                    onClick={() => setCanceling(item)}
                  >
                    {t('webmail.scheduled.cancel')}
                  </Button>
                </div>
              </li>
            ))}
          </ul>
        )}
      </Card>
      {rescheduling ? (
        <ScheduleDialog
          title={t('webmail.scheduled.rescheduleTitle')}
          confirmLabel={t('webmail.scheduled.reschedule')}
          initial={new Date(rescheduling.send_at)}
          onClose={() => setRescheduling(null)}
          onConfirm={(sendAt) =>
            guarded(async () => {
              const updated = await webmailApi.reschedule(rescheduling.id, sendAt);
              scheduled.setData((current) =>
                (current ?? []).map((row) => (row.id === updated.id ? updated : row)),
              );
              toast.success(t('webmail.schedule.done', { when: formatScheduled(updated.send_at) }));
              setRescheduling(null);
            })
          }
        />
      ) : null}
      <ConfirmDialog
        open={canceling !== null}
        title={t('webmail.scheduled.cancelTitle')}
        message={t('webmail.scheduled.cancelConfirm')}
        confirmLabel={t('webmail.scheduled.cancel')}
        danger
        onCancel={() => setCanceling(null)}
        onConfirm={() =>
          guarded(async () => {
            if (!canceling) return;
            await webmailApi.cancelScheduled(canceling.id);
            scheduled.setData((current) =>
              (current ?? []).filter((row) => row.id !== canceling.id),
            );
            toast.success(t('webmail.scheduled.canceled'));
            setCanceling(null);
            reloadFolders();
          })
        }
      />
    </div>
  );
}
