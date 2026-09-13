import { useEffect, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import {
  FLAGS,
  FOLDER_ROLES,
  webmailApi,
  type FlagChange,
  type WebmailFolder,
} from '@/api/webmail';
import { errorMessage } from '@/api/messages';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import {
  Button,
  ConfirmDialog,
  ErrorState,
  FormField,
  Modal,
  Select,
  Skeleton,
  useToast,
} from '@/design/components';
import {
  IconChevronLeft,
  IconEdit,
  IconFolder,
  IconForward,
  IconMailOpen,
  IconReply,
  IconReplyAll,
  IconStar,
  IconTrash,
} from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { applyFlagChange } from './flags';
import { orderFolders } from './folders';
import { addressList } from './format';
import { MessageBody } from './MessageBody';

export interface MessageViewProps {
  folderName: string;
  folder: WebmailFolder | null;
  folders: readonly WebmailFolder[];
  uid: number;
  backHref: string;
  /** El primer acceso marco el mensaje como leido en el servidor. */
  onSeen: (uid: number) => void;
  onFlagsChanged: (uid: number, flags: string[]) => void;
  /** El mensaje se movio o se borro: ya no esta en esta carpeta. */
  onGone: () => void;
}

export function MessageView({
  folderName,
  folder,
  folders,
  uid,
  backHref,
  onSeen,
  onFlagsChanged,
  onGone,
}: MessageViewProps) {
  const toast = useToast();
  const navigate = useNavigate();
  // Las imagenes remotas se piden por mensaje; la segunda lectura no vuelve a marcarlo.
  const [remote, setRemote] = useState(false);
  const [flags, setFlags] = useState<string[] | null>(null);
  const [moving, setMoving] = useState(false);
  const [purging, setPurging] = useState(false);

  const message = useQuery(
    (signal) =>
      webmailApi.message(folderName, uid, { peek: remote, allowRemoteImages: remote }, signal),
    [folderName, uid, remote],
  );
  const data = message.data;

  useEffect(() => {
    if (!data || flags !== null) return;
    setFlags(applyFlagChange(data.flags, { add: [FLAGS.seen] }));
    onSeen(uid);
  }, [data, flags, onSeen, uid]);

  const changeFlags = useAction(async (change: FlagChange) => {
    await webmailApi.setFlags(folderName, uid, change);
    const next = applyFlagChange(flags ?? data?.flags ?? [], change);
    setFlags(next);
    onFlagsChanged(uid, next);
  });

  const trash = useAction(async () => {
    const { permanent } = await webmailApi.remove(folderName, uid);
    toast.success(t(permanent ? 'webmail.reader.deleted' : 'webmail.reader.trashed'));
    onGone();
  });

  const back = (
    <Link to={backHref} className="cf-btn cf-btn--ghost cf-btn--sm cf-wm-reader__back">
      <IconChevronLeft size={16} />
      {t('webmail.reader.back')}
    </Link>
  );

  if (!data) {
    return (
      <div className="cf-wm-reader">
        {back}
        {message.error ? (
          <ErrorState error={message.error} onRetry={message.reload} />
        ) : (
          <Skeleton lines={10} />
        )}
      </div>
    );
  }

  const isTrash = folder?.role === FOLDER_ROLES.trash;
  const isDrafts = folder?.role === FOLDER_ROLES.drafts;
  const seen = flags?.includes(FLAGS.seen) ?? true;
  const flagged = flags?.includes(FLAGS.flagged) ?? false;
  const actionError = changeFlags.error ?? trash.error;
  const compose = (mode: string) => navigate(paths.webmailComposeFrom(mode, folderName, uid));
  const seenLabel = t(seen ? 'webmail.reader.markUnread' : 'webmail.reader.markRead');
  const deleteLabel = t(isTrash ? 'webmail.reader.deleteForever' : 'webmail.reader.delete');

  return (
    <article className="cf-wm-reader" aria-labelledby="wm-subject">
      <div
        className="cf-wm-reader__toolbar"
        role="toolbar"
        aria-label={t('webmail.reader.actions')}
      >
        {back}
        {isDrafts ? (
          <Button
            size="sm"
            variant="primary"
            icon={<IconEdit size={16} />}
            onClick={() => compose('draft')}
          >
            {t('webmail.reader.editDraft')}
          </Button>
        ) : (
          <>
            <Button size="sm" icon={<IconReply size={16} />} onClick={() => compose('reply')}>
              {t('webmail.reader.reply')}
            </Button>
            <Button size="sm" icon={<IconReplyAll size={16} />} onClick={() => compose('replyAll')}>
              {t('webmail.reader.replyAll')}
            </Button>
            <Button size="sm" icon={<IconForward size={16} />} onClick={() => compose('forward')}>
              {t('webmail.reader.forward')}
            </Button>
          </>
        )}
        <span className="cf-wm-reader__spacer" />
        <Button
          size="sm"
          variant="ghost"
          iconOnly
          title={seenLabel}
          icon={<IconMailOpen size={16} />}
          disabled={changeFlags.busy}
          onClick={() =>
            void changeFlags.run(seen ? { remove: [FLAGS.seen] } : { add: [FLAGS.seen] })
          }
        >
          {seenLabel}
        </Button>
        <Button
          size="sm"
          variant="ghost"
          iconOnly
          className="cf-wm-flag"
          title={t('webmail.reader.flag')}
          aria-pressed={flagged}
          icon={<IconStar size={16} />}
          disabled={changeFlags.busy}
          onClick={() =>
            void changeFlags.run(flagged ? { remove: [FLAGS.flagged] } : { add: [FLAGS.flagged] })
          }
        >
          {t('webmail.reader.flag')}
        </Button>
        <Button
          size="sm"
          variant="ghost"
          iconOnly
          title={t('webmail.reader.move')}
          icon={<IconFolder size={16} />}
          onClick={() => setMoving(true)}
        >
          {t('webmail.reader.move')}
        </Button>
        <Button
          size="sm"
          variant="ghost"
          iconOnly
          title={deleteLabel}
          icon={<IconTrash size={16} />}
          loading={trash.busy}
          onClick={() => (isTrash ? setPurging(true) : void trash.run())}
        >
          {deleteLabel}
        </Button>
      </div>
      {actionError ? (
        <div className="cf-form__error" role="alert">
          {errorMessage(actionError)}
        </div>
      ) : null}
      {message.error ? (
        <div className="cf-form__error" role="alert">
          {errorMessage(message.error)}
        </div>
      ) : null}
      <header className="cf-wm-reader__header">
        <h2 id="wm-subject" className="cf-wm-reader__subject">
          {data.subject || t('webmail.noSubject')}
        </h2>
        <dl className="cf-dl cf-wm-reader__meta">
          <HeaderRow label={t('webmail.header.from')} value={addressList(data.from)} />
          <HeaderRow label={t('webmail.header.to')} value={addressList(data.to)} />
          {data.cc.length ? (
            <HeaderRow label={t('webmail.header.cc')} value={addressList(data.cc)} />
          ) : null}
          {data.bcc.length ? (
            <HeaderRow label={t('webmail.header.bcc')} value={addressList(data.bcc)} />
          ) : null}
          {data.reply_to.length ? (
            <HeaderRow label={t('webmail.header.replyTo')} value={addressList(data.reply_to)} />
          ) : null}
          <HeaderRow label={t('webmail.header.date')} value={formatDateTime(data.date)} />
        </dl>
      </header>
      <MessageBody
        message={data}
        remoteAllowed={remote}
        remoteLoading={remote && message.loading}
        onAllowRemote={() => setRemote(true)}
      />
      {moving ? (
        <MoveDialog
          folders={folders}
          current={folderName}
          onClose={() => setMoving(false)}
          onMove={async (to, label) => {
            await webmailApi.move(folderName, uid, to);
            toast.success(t('webmail.move.done', { folder: label }));
            setMoving(false);
            onGone();
          }}
        />
      ) : null}
      <ConfirmDialog
        open={purging}
        title={t('webmail.reader.deleteForever')}
        message={t('webmail.reader.deleteForeverConfirm')}
        confirmLabel={t('webmail.reader.deleteForever')}
        danger
        onCancel={() => setPurging(false)}
        onConfirm={async () => {
          const { permanent } = await webmailApi.remove(folderName, uid);
          toast.success(t(permanent ? 'webmail.reader.deleted' : 'webmail.reader.trashed'));
          setPurging(false);
          onGone();
        }}
      />
    </article>
  );
}

function HeaderRow({ label, value }: { label: string; value: string }) {
  return (
    <>
      <dt>{label}</dt>
      <dd>{value || t('common.dash')}</dd>
    </>
  );
}

function MoveDialog({
  folders,
  current,
  onClose,
  onMove,
}: {
  folders: readonly WebmailFolder[];
  current: string;
  onClose: () => void;
  onMove: (to: string, label: string) => Promise<void>;
}) {
  const targets = orderFolders(folders).filter(
    (item) => item.folder.selectable && item.folder.name !== current,
  );
  const [to, setTo] = useState(targets[0]?.folder.name ?? '');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const target = targets.find((item) => item.folder.name === to);

  const submit = async () => {
    if (!target) return;
    setBusy(true);
    setError(null);
    try {
      await onMove(target.folder.name, target.label);
    } catch (err) {
      setError(err);
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      title={t('webmail.move.title')}
      onClose={() => (busy ? undefined : onClose())}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button variant="primary" loading={busy} disabled={!target} onClick={() => void submit()}>
            {t('webmail.move.submit')}
          </Button>
        </>
      }
    >
      <div className="cf-stack" style={{ gap: 'var(--cf-space-3)' }}>
        {targets.length ? (
          <FormField label={t('webmail.move.to')} htmlFor="wm-move-to">
            <Select
              id="wm-move-to"
              options={targets.map((item) => ({
                value: item.folder.name,
                label: `${'  '.repeat(item.depth)}${item.label}`,
              }))}
              value={to}
              onChange={(e) => setTo(e.target.value)}
            />
          </FormField>
        ) : (
          <p className="cf-modal__message">{t('webmail.move.none')}</p>
        )}
        {error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(error)}
          </div>
        ) : null}
      </div>
    </Modal>
  );
}
