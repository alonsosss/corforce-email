import { useState } from 'react';
import { Link } from 'react-router-dom';
import { ERROR_CODES, errorCode } from '@/api/errors';
import { webmailRemindersApi, type FollowUp, type SnoozedMessage } from '@/api/webmail';
import { useQuery } from '@/hooks/useQuery';
import {
  Button,
  Card,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  PageHeader,
  Skeleton,
  useToast,
} from '@/design/components';
import { IconBell, IconClock, IconInbox, IconX } from '@/design/icons';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { formatScheduled } from './schedule';
import { SnoozeDialog } from './SnoozeDialog';
import { useWebmailOutlet } from './webmailContext';

/**
 * Pospuestos (vuelven a su carpeta a su hora, sin leer) y seguimientos pendientes (vuelven a la
 * entrada si nadie responde). Es la carpeta Pospuestos de la navegacion.
 */
export default function SnoozedPage() {
  return (
    <div className="cf-wm-page">
      <PageHeader
        title={t('webmail.snoozed.title')}
        description={t('webmail.snoozed.description')}
        back={{ to: paths.webmail, label: t('webmail.settings.back') }}
      />
      <SnoozedList />
      <FollowUpList />
    </div>
  );
}

/** Una fila que ya volvio o se cancelo en otro dispositivo: se relee la lista y se muestra el error. */
function isStale(err: unknown): boolean {
  const code = errorCode(err);
  return code === ERROR_CODES.REMINDER_NOT_FOUND || code === ERROR_CODES.REMINDER_NOT_PENDING;
}

function SnoozedList() {
  const toast = useToast();
  const { reloadFolders } = useWebmailOutlet();
  const snoozed = useQuery((signal) => webmailRemindersApi.snoozed(signal), []);
  const [rescheduling, setRescheduling] = useState<SnoozedMessage | null>(null);
  const [returning, setReturning] = useState<SnoozedMessage | null>(null);
  const items = snoozed.data ?? [];

  const guarded = async (action: () => Promise<void>) => {
    try {
      await action();
    } catch (err) {
      if (isStale(err)) snoozed.reload();
      throw err;
    }
  };

  return (
    <Card flush title={t('webmail.snoozed.messages')}>
      {snoozed.error && !snoozed.data ? (
        <ErrorState error={snoozed.error} onRetry={snoozed.reload} />
      ) : !snoozed.data ? (
        <div className="cf-wm-pad">
          <Skeleton lines={3} />
        </div>
      ) : items.length === 0 ? (
        <EmptyState
          icon={<IconBell size={32} />}
          title={t('webmail.snoozed.empty')}
          description={t('webmail.snoozed.emptyHint')}
        />
      ) : (
        <ul className="cf-wm-scheduled">
          {items.map((item) => (
            <li key={item.id} className="cf-wm-scheduled__item">
              <div className="cf-wm-scheduled__main">
                <Link
                  className="cf-wm-scheduled__subject"
                  to={paths.webmailView({ folder: item.folder, uid: item.uid })}
                >
                  {item.subject || t('webmail.noSubject')}
                </Link>
                <span className="cf-text-sm cf-text-secondary cf-truncate">
                  {item.from || t('common.dash')}
                </span>
              </div>
              <span className="cf-wm-scheduled__when">
                <IconClock size={14} />
                {t('webmail.snoozed.returnsAt', { when: formatScheduled(item.until) })}
              </span>
              <div className="cf-wm-scheduled__actions">
                <Button size="sm" onClick={() => setRescheduling(item)}>
                  {t('webmail.snoozed.reschedule')}
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  icon={<IconInbox size={16} />}
                  onClick={() => setReturning(item)}
                >
                  {t('webmail.snoozed.returnNow')}
                </Button>
              </div>
            </li>
          ))}
        </ul>
      )}
      {rescheduling ? (
        <SnoozeDialog
          title={t('webmail.snoozed.rescheduleTitle')}
          confirmLabel={t('webmail.snoozed.reschedule')}
          initial={new Date(rescheduling.until)}
          onClose={() => setRescheduling(null)}
          onConfirm={(until) =>
            guarded(async () => {
              const updated = await webmailRemindersApi.reschedule(rescheduling.id, until);
              snoozed.setData((current) =>
                (current ?? []).map((row) => (row.id === updated.id ? updated : row)),
              );
              toast.success(
                t('webmail.snooze.done', { n: 1, when: formatScheduled(updated.until) }),
              );
              setRescheduling(null);
            })
          }
        />
      ) : null}
      <ConfirmDialog
        open={returning !== null}
        title={t('webmail.snoozed.returnNow')}
        message={t('webmail.snoozed.returnConfirm')}
        confirmLabel={t('webmail.snoozed.returnNow')}
        onCancel={() => setReturning(null)}
        onConfirm={() =>
          guarded(async () => {
            if (!returning) return;
            await webmailRemindersApi.unsnooze(returning.id);
            snoozed.setData((current) => (current ?? []).filter((row) => row.id !== returning.id));
            toast.success(t('webmail.snoozed.returned'));
            setReturning(null);
            reloadFolders();
          })
        }
      />
    </Card>
  );
}

function FollowUpList() {
  const toast = useToast();
  const followUps = useQuery((signal) => webmailRemindersApi.followUps(signal), []);
  const [canceling, setCanceling] = useState<FollowUp | null>(null);
  const items = followUps.data ?? [];

  return (
    <Card flush title={t('webmail.followUps.title')}>
      {followUps.error && !followUps.data ? (
        <ErrorState error={followUps.error} onRetry={followUps.reload} />
      ) : !followUps.data ? (
        <div className="cf-wm-pad">
          <Skeleton lines={3} />
        </div>
      ) : items.length === 0 ? (
        <EmptyState
          icon={<IconClock size={32} />}
          title={t('webmail.followUps.empty')}
          description={t('webmail.followUps.emptyHint')}
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
                {t('webmail.followUps.dueAt', { when: formatScheduled(item.due_at) })}
              </span>
              <div className="cf-wm-scheduled__actions">
                <Button
                  size="sm"
                  variant="ghost"
                  icon={<IconX size={16} />}
                  onClick={() => setCanceling(item)}
                >
                  {t('webmail.followUps.cancel')}
                </Button>
              </div>
            </li>
          ))}
        </ul>
      )}
      <ConfirmDialog
        open={canceling !== null}
        title={t('webmail.followUps.cancel')}
        message={t('webmail.followUps.cancelConfirm')}
        confirmLabel={t('webmail.followUps.cancel')}
        onCancel={() => setCanceling(null)}
        onConfirm={async () => {
          if (!canceling) return;
          try {
            await webmailRemindersApi.cancelFollowUp(canceling.id);
          } catch (err) {
            if (isStale(err)) followUps.reload();
            throw err;
          }
          followUps.setData((current) => (current ?? []).filter((row) => row.id !== canceling.id));
          toast.success(t('webmail.followUps.canceled'));
          setCanceling(null);
        }}
      />
    </Card>
  );
}
