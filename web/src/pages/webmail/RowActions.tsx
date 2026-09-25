import { useState, type ReactNode } from 'react';
import { errorMessage } from '@/api/messages';
import { Button, useToast } from '@/design/components';
import { IconArchive, IconBell, IconMail, IconMailOpen, IconTrash } from '@/design/icons';
import { t, type MessageKey } from '@/i18n';

export interface RowActionsProps {
  unread: boolean;
  deleteLabel: MessageKey;
  onArchive?: () => Promise<void>;
  onDelete: () => Promise<void>;
  onToggleRead: () => Promise<void>;
  onSnooze?: () => void;
}

/** Acciones rapidas de una fila del listado; aparecen al pasar el puntero o con el foco. */
export function RowActions({
  unread,
  deleteLabel,
  onArchive,
  onDelete,
  onToggleRead,
  onSnooze,
}: RowActionsProps) {
  const toast = useToast();
  const [busy, setBusy] = useState(false);

  const run = (action: () => Promise<void>) => async () => {
    setBusy(true);
    try {
      await action();
    } catch (err) {
      toast.error(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const action = (label: MessageKey, icon: ReactNode, onClick: () => void) => (
    <Button
      size="sm"
      variant="ghost"
      iconOnly
      title={t(label)}
      icon={icon}
      disabled={busy}
      onClick={onClick}
    >
      {t(label)}
    </Button>
  );

  return (
    <>
      {onArchive
        ? action('webmail.batch.archive', <IconArchive size={16} />, run(onArchive))
        : null}
      {action(deleteLabel, <IconTrash size={16} />, run(onDelete))}
      {action(
        unread ? 'webmail.batch.markRead' : 'webmail.batch.markUnread',
        unread ? <IconMailOpen size={16} /> : <IconMail size={16} />,
        run(onToggleRead),
      )}
      {onSnooze ? action('webmail.snooze.action', <IconBell size={16} />, onSnooze) : null}
    </>
  );
}
