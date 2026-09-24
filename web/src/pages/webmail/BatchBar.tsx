import { useState, type ReactNode } from 'react';
import { FLAGS, FOLDER_ROLES, type FlagChange, type WebmailFolder } from '@/api/webmail';
import { errorMessage } from '@/api/messages';
import { Button } from '@/design/components';
import {
  IconBan,
  IconCheckSquare,
  IconFolder,
  IconInbox,
  IconMail,
  IconMailOpen,
  IconStar,
  IconTrash,
} from '@/design/icons';
import { t, type MessageKey } from '@/i18n';
import { folderWithRole } from './folders';
import { MoveDialog } from './MoveDialog';

export interface BatchBarProps {
  role: string;
  folderName: string;
  folders: readonly WebmailFolder[];
  uids: number[];
  onFlags: (uids: number[], change: FlagChange) => Promise<void>;
  onMove: (uids: number[], to: WebmailFolder, done: MessageKey) => Promise<void>;
  onDelete: (uids: number[]) => Promise<void>;
}

/** Acciones sobre los mensajes marcados de la pagina. */
export function BatchBar({
  role,
  folderName,
  folders,
  uids,
  onFlags,
  onMove,
  onDelete,
}: BatchBarProps) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [moving, setMoving] = useState(false);
  const junk = folderWithRole(folders, FOLDER_ROLES.junk);
  const inbox = folderWithRole(folders, FOLDER_ROLES.inbox);
  const isJunk = role === FOLDER_ROLES.junk;
  const canReportSpam =
    Boolean(junk) && !isJunk && role !== FOLDER_ROLES.drafts && role !== FOLDER_ROLES.sent;

  const run = async (action: () => Promise<void>) => {
    setBusy(true);
    setError(null);
    try {
      await action();
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  const button = (label: MessageKey, icon: ReactNode, action: () => Promise<void>) => (
    <Button
      size="sm"
      variant="ghost"
      iconOnly
      title={t(label)}
      icon={icon}
      disabled={busy}
      onClick={() => void run(action)}
    >
      {t(label)}
    </Button>
  );

  return (
    <div className="cf-wm-batch" role="toolbar" aria-label={t('webmail.batch.actions')}>
      <span className="cf-wm-batch__count">
        <IconCheckSquare size={16} />
        {t('webmail.batch.selected', { n: uids.length })}
      </span>
      {button('webmail.batch.markRead', <IconMailOpen size={16} />, () =>
        onFlags(uids, { add: [FLAGS.seen] }),
      )}
      {button('webmail.batch.markUnread', <IconMail size={16} />, () =>
        onFlags(uids, { remove: [FLAGS.seen] }),
      )}
      {button('webmail.batch.flag', <IconStar size={16} />, () =>
        onFlags(uids, { add: [FLAGS.flagged] }),
      )}
      {button('webmail.batch.unflag', <IconStar size={16} className="cf-wm-unflag" />, () =>
        onFlags(uids, { remove: [FLAGS.flagged] }),
      )}
      <Button
        size="sm"
        variant="ghost"
        iconOnly
        title={t('webmail.batch.move')}
        icon={<IconFolder size={16} />}
        disabled={busy}
        onClick={() => setMoving(true)}
      >
        {t('webmail.batch.move')}
      </Button>
      {canReportSpam && junk
        ? button('webmail.batch.reportSpam', <IconBan size={16} />, () =>
            onMove(uids, junk, 'webmail.batch.spamDone'),
          )
        : null}
      {isJunk && inbox
        ? button('webmail.batch.notSpam', <IconInbox size={16} />, () =>
            onMove(uids, inbox, 'webmail.batch.notSpamDone'),
          )
        : null}
      {button(
        role === FOLDER_ROLES.trash ? 'webmail.reader.deleteForever' : 'webmail.batch.delete',
        <IconTrash size={16} />,
        () => onDelete(uids),
      )}
      {error ? (
        <div className="cf-form__error cf-wm-batch__error" role="alert">
          {errorMessage(error)}
        </div>
      ) : null}
      {moving ? (
        <MoveDialog
          title={t('webmail.batch.moveTitle', { n: uids.length })}
          folders={folders}
          current={folderName}
          onClose={() => setMoving(false)}
          onMove={async (to) => {
            const target = folders.find((f) => f.name === to);
            if (!target) return;
            await onMove(uids, target, 'webmail.batch.moved');
            setMoving(false);
          }}
        />
      ) : null}
    </div>
  );
}
