import { useEffect, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import {
  FLAGS,
  FOLDER_ROLES,
  webmailApi,
  type FlagChange,
  type MailAddress,
  type WebmailFolder,
} from '@/api/webmail';
import { errorMessage } from '@/api/messages';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import { Button, ConfirmDialog, ErrorState, Skeleton, useToast } from '@/design/components';
import {
  IconBan,
  IconChevronLeft,
  IconDownload,
  IconEdit,
  IconFolder,
  IconForward,
  IconInbox,
  IconMailOpen,
  IconPrinter,
  IconReply,
  IconReplyAll,
  IconStar,
  IconTrash,
  IconUserPlus,
} from '@/design/icons';
import { saveBlob } from '@/lib/download';
import { formatDateTime } from '@/lib/format';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { ContactFormDialog } from './contacts/ContactFormDialog';
import { contactFromSender } from './contacts/contacts';
import { applyFlagChange } from './flags';
import { folderWithRole } from './folders';
import { addressList } from './format';
import { MessageBody } from './MessageBody';
import { MoveDialog } from './MoveDialog';
import { printMessage } from './print';

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

/** Nombre del .eml cuando el servicio no manda uno. */
function rawFilename(subject: string, uid: number): string {
  const base = subject
    .replace(/[^\p{L}\p{N} _-]+/gu, '')
    .trim()
    .slice(0, 60);
  return `${base || t('webmail.reader.rawDefault', { uid })}.eml`;
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
  const [newContact, setNewContact] = useState<MailAddress | null>(null);

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

  // Spam y no spam es mover a y desde Spam: Dovecot avisa a Rspamd para que aprenda.
  const junk = folderWithRole(folders, FOLDER_ROLES.junk);
  const inbox = folderWithRole(folders, FOLDER_ROLES.inbox);
  const reclassify = useAction(async (to: WebmailFolder, done: string) => {
    await webmailApi.move(folderName, uid, to.name);
    toast.success(done);
    onGone();
  });

  const download = useAction(async () => {
    const raw = await webmailApi.downloadRaw(folderName, uid);
    saveBlob(raw.blob, raw.filename ?? rawFilename(data?.subject ?? '', uid));
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

  const role = folder?.role ?? '';
  const isTrash = role === FOLDER_ROLES.trash;
  const isDrafts = role === FOLDER_ROLES.drafts;
  const isJunk = role === FOLDER_ROLES.junk;
  const canReportSpam = Boolean(junk) && !isJunk && !isDrafts && role !== FOLDER_ROLES.sent;
  const seen = flags?.includes(FLAGS.seen) ?? true;
  const flagged = flags?.includes(FLAGS.flagged) ?? false;
  const actionError = changeFlags.error ?? trash.error ?? reclassify.error ?? download.error;
  const compose = (mode: string) => navigate(paths.webmailComposeFrom(mode, folderName, uid));
  const seenLabel = t(seen ? 'webmail.reader.markUnread' : 'webmail.reader.markRead');
  const deleteLabel = t(isTrash ? 'webmail.reader.deleteForever' : 'webmail.reader.delete');
  const sender = data.from[0];

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
        {canReportSpam && junk ? (
          <Button
            size="sm"
            variant="ghost"
            iconOnly
            title={t('webmail.reader.spam')}
            icon={<IconBan size={16} />}
            loading={reclassify.busy}
            onClick={() => void reclassify.run(junk, t('webmail.reader.spamDone'))}
          >
            {t('webmail.reader.spam')}
          </Button>
        ) : null}
        {isJunk && inbox ? (
          <Button
            size="sm"
            variant="ghost"
            iconOnly
            title={t('webmail.reader.notSpam')}
            icon={<IconInbox size={16} />}
            loading={reclassify.busy}
            onClick={() => void reclassify.run(inbox, t('webmail.reader.notSpamDone'))}
          >
            {t('webmail.reader.notSpam')}
          </Button>
        ) : null}
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
        <Button
          size="sm"
          variant="ghost"
          iconOnly
          title={t('webmail.reader.print')}
          icon={<IconPrinter size={16} />}
          onClick={() => printMessage(data, { allowRemoteImages: remote })}
        >
          {t('webmail.reader.print')}
        </Button>
        <Button
          size="sm"
          variant="ghost"
          iconOnly
          title={t('webmail.reader.raw')}
          icon={<IconDownload size={16} />}
          loading={download.busy}
          onClick={() => void download.run()}
        >
          {t('webmail.reader.raw')}
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
          <dt>{t('webmail.header.from')}</dt>
          <dd className="cf-wm-reader__from">
            <span>{addressList(data.from) || t('common.dash')}</span>
            {sender ? (
              <Button
                size="sm"
                variant="ghost"
                iconOnly
                title={t('webmail.reader.addContact', { address: sender.email })}
                icon={<IconUserPlus size={14} />}
                onClick={() => setNewContact(sender)}
              >
                {t('webmail.reader.addContact', { address: sender.email })}
              </Button>
            ) : null}
          </dd>
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
          title={t('webmail.move.title')}
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
      {newContact ? (
        <ContactFormDialog
          seed={contactFromSender(newContact)}
          onClose={() => setNewContact(null)}
          onSaved={() => setNewContact(null)}
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
