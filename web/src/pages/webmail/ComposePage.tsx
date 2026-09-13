import { useEffect, useRef, useState, type FormEvent } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { FOLDER_ROLES, webmailApi, type ComposeInput, type MailMessage } from '@/api/webmail';
import { errorMessage } from '@/api/messages';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import {
  Button,
  ChipsInput,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  FormField,
  Input,
  Skeleton,
  Textarea,
  useToast,
} from '@/design/components';
import { IconChevronLeft, IconPaperclip, IconSend, IconX } from '@/design/icons';
import { formatBytes } from '@/lib/quota';
import { t, type MessageKey } from '@/i18n';
import { paths } from '@/paths';
import { useWebmailStore } from '@/webmail/store';
import {
  buildDraft,
  EMPTY_DRAFT,
  normalizeRecipient,
  parseComposeMode,
  type ComposeMode,
  type DraftSeed,
} from './compose';
import { folderWithRole } from './folders';
import { displayFilename, parsePositiveInt } from './format';
import { referencedInlineParts } from './inlineImages';
import { useWebmailOutlet } from './webmailContext';

const TITLES: Record<ComposeMode | 'new', MessageKey> = {
  new: 'webmail.compose.title.new',
  reply: 'webmail.compose.title.reply',
  replyAll: 'webmail.compose.title.replyAll',
  forward: 'webmail.compose.title.forward',
  draft: 'webmail.compose.title.draft',
};

/**
 * Adjuntos del original para reenviar o seguir un borrador: se descargan con la sesion y
 * se vuelven a subir, y el servicio los analiza de nuevo antes de enviarlos o guardarlos.
 * Las imagenes en linea del HTML no se reenvian: el reenvio va en texto.
 */
async function originalAttachments(message: MailMessage, signal: AbortSignal): Promise<File[]> {
  const inline = new Set(referencedInlineParts(message).values());
  const parts = message.attachments.filter((part) => !inline.has(part));
  return Promise.all(
    parts.map(async (part) => {
      const { blob, filename } = await webmailApi.downloadPart(
        message.folder,
        message.uid,
        part.part,
        signal,
      );
      const name = displayFilename(filename ?? part.filename, t('webmail.attachments.unnamed'));
      return new File([blob], name, { type: part.content_type || blob.type });
    }),
  );
}

export default function ComposePage() {
  const [params] = useSearchParams();
  const rawMode = params.get('mode');
  const mode = parseComposeMode(rawMode);
  const folder = params.get('folder');
  const uid = parsePositiveInt(params.get('uid'));
  const username = useWebmailStore((s) => s.session?.username ?? '');

  const source = useQuery(
    async (signal) => {
      if (!mode || !folder || !uid) return null;
      const message = await webmailApi.message(folder, uid, { peek: true }, signal);
      const files =
        mode === 'forward' || mode === 'draft' ? await originalAttachments(message, signal) : [];
      return { message, files };
    },
    [mode, folder, uid],
  );

  if ((rawMode && !mode) || (mode && (!folder || !uid))) {
    return (
      <div className="cf-wm-compose">
        <EmptyState
          title={t('webmail.compose.invalidLink')}
          action={
            <Link className="cf-btn cf-btn--secondary" to={paths.webmail}>
              {t('webmail.reader.back')}
            </Link>
          }
        />
      </div>
    );
  }
  if (mode && source.error) {
    return (
      <div className="cf-wm-compose">
        <ErrorState error={source.error} onRetry={source.reload} />
      </div>
    );
  }
  if (mode && !source.data) {
    return (
      <div className="cf-wm-compose">
        <Skeleton lines={10} />
      </div>
    );
  }

  const seed = mode && source.data ? buildDraft(mode, source.data.message, username) : EMPTY_DRAFT;
  const backHref =
    folder && uid
      ? paths.webmailView({ folder, uid: mode === 'draft' ? undefined : uid })
      : paths.webmail;
  return (
    <ComposeForm
      key={`${mode ?? 'new'}:${folder ?? ''}:${uid ?? ''}`}
      mode={mode}
      seed={seed}
      initialFiles={source.data?.files ?? []}
      backHref={backHref}
    />
  );
}

function ComposeForm({
  mode,
  seed,
  initialFiles,
  backHref,
}: {
  mode: ComposeMode | null;
  seed: DraftSeed;
  initialFiles: File[];
  backHref: string;
}) {
  const toast = useToast();
  const navigate = useNavigate();
  const { folders, reloadFolders } = useWebmailOutlet();
  const refreshSession = useWebmailStore((s) => s.refresh);
  const [to, setTo] = useState(seed.to);
  const [cc, setCc] = useState(seed.cc);
  const [bcc, setBcc] = useState(seed.bcc);
  const [showCopies, setShowCopies] = useState(seed.cc.length > 0 || seed.bcc.length > 0);
  const [subject, setSubject] = useState(seed.subject);
  const [text, setText] = useState(seed.text);
  const [files, setFiles] = useState<File[]>(initialFiles);
  const [draftUid, setDraftUid] = useState(seed.draftUid);
  const [recipientError, setRecipientError] = useState<string | null>(null);
  const [dirty, setDirty] = useState(false);
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const bodyRef = useRef<HTMLTextAreaElement>(null);

  useEffect(() => {
    // Al responder se escribe encima de la cita; en lo demas se empieza por el destinatario.
    if (seed.inReplyTo && bodyRef.current) {
      bodyRef.current.focus();
      bodyRef.current.setSelectionRange(0, 0);
    } else {
      document.getElementById('compose-to')?.focus();
    }
  }, [seed.inReplyTo]);

  const edit =
    <T,>(setter: (value: T) => void) =>
    (value: T) => {
      setter(value);
      setDirty(true);
    };

  const input = (): ComposeInput => ({
    to,
    cc,
    bcc,
    subject: subject.trim(),
    text,
    inReplyTo: seed.inReplyTo,
    attachments: files,
  });

  const send = useAction(async () => {
    const result = await webmailApi.send(input());
    toast.success(t('webmail.compose.sent'));
    if (!result.saved_to_sent) toast.info(t('webmail.compose.notSavedToSent'));
    const drafts = folders.data ? folderWithRole(folders.data, FOLDER_ROLES.drafts) : undefined;
    if (draftUid && drafts) {
      // El servicio no retira el borrador al enviar: se lleva a la papelera. Si falla, el
      // mensaje ya salio y el borrador solo queda de mas.
      await webmailApi.remove(drafts.name, draftUid).catch(() => undefined);
    }
    reloadFolders();
    void refreshSession();
    navigate(backHref, { replace: true });
  });

  const save = useAction(async () => {
    const { uid } = await webmailApi.saveDraft(input(), draftUid);
    setDraftUid(uid);
    setDirty(false);
    toast.success(t('webmail.compose.draftSaved'));
    reloadFolders();
    void refreshSession();
  });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (to.length + cc.length + bcc.length === 0) {
      setRecipientError(t('webmail.compose.noRecipients'));
      return;
    }
    setRecipientError(null);
    save.clearError();
    await send.run();
  };

  const busy = send.busy || save.busy;
  const error = send.error ?? save.error;
  const chips = {
    normalize: normalizeRecipient,
    removeLabel: (value: string) => t('common.removeValue', { value }),
    rejectedLabel: (rejected: string[]) =>
      t('webmail.compose.invalidAddresses', { list: rejected.join(', ') }),
    placeholder: t('webmail.compose.recipientsPlaceholder'),
    disabled: busy,
  };

  return (
    <form
      className="cf-wm-compose cf-form"
      onSubmit={(e) => void submit(e)}
      noValidate
      aria-labelledby="wm-compose-title"
    >
      <div className="cf-wm-compose__head">
        <Link to={backHref} className="cf-btn cf-btn--ghost cf-btn--sm">
          <IconChevronLeft size={16} />
          {t('webmail.reader.back')}
        </Link>
        <h1 id="wm-compose-title" className="cf-wm-compose__title">
          {t(TITLES[mode ?? 'new'])}
        </h1>
      </div>
      <FormField label={t('webmail.header.to')} htmlFor="compose-to" error={recipientError}>
        <ChipsInput
          id="compose-to"
          values={to}
          onChange={(values) => {
            edit(setTo)(values);
            setRecipientError(null);
          }}
          invalid={Boolean(recipientError)}
          {...chips}
        />
      </FormField>
      {showCopies ? (
        <div className="cf-form__row">
          <FormField label={t('webmail.header.cc')} htmlFor="compose-cc">
            <ChipsInput id="compose-cc" values={cc} onChange={edit(setCc)} {...chips} />
          </FormField>
          <FormField label={t('webmail.header.bcc')} htmlFor="compose-bcc">
            <ChipsInput id="compose-bcc" values={bcc} onChange={edit(setBcc)} {...chips} />
          </FormField>
        </div>
      ) : (
        <div>
          <Button size="sm" variant="ghost" onClick={() => setShowCopies(true)}>
            {t('webmail.compose.addCopies')}
          </Button>
        </div>
      )}
      <FormField label={t('webmail.header.subject')} htmlFor="compose-subject">
        <Input
          id="compose-subject"
          value={subject}
          onChange={(e) => edit(setSubject)(e.target.value)}
          disabled={busy}
        />
      </FormField>
      <FormField label={t('webmail.compose.body')} htmlFor="compose-body">
        <Textarea
          ref={bodyRef}
          id="compose-body"
          rows={14}
          value={text}
          onChange={(e) => edit(setText)(e.target.value)}
          disabled={busy}
        />
      </FormField>
      <AttachmentPicker files={files} onChange={edit(setFiles)} disabled={busy} />
      {error ? (
        <div className="cf-form__error" role="alert">
          {errorMessage(error)}
        </div>
      ) : null}
      <div className="cf-form__actions cf-wm-compose__actions">
        <Button
          variant="ghost"
          disabled={busy}
          onClick={() => (dirty ? setConfirmDiscard(true) : navigate(backHref))}
        >
          {t('webmail.compose.discard')}
        </Button>
        <Button
          loading={save.busy}
          disabled={send.busy}
          onClick={() => {
            send.clearError();
            void save.run();
          }}
        >
          {t('webmail.compose.saveDraft')}
        </Button>
        <Button
          type="submit"
          variant="primary"
          icon={<IconSend size={16} />}
          loading={send.busy}
          disabled={save.busy}
        >
          {t('webmail.compose.send')}
        </Button>
      </div>
      <ConfirmDialog
        open={confirmDiscard}
        title={t('webmail.compose.discardTitle')}
        message={t('webmail.compose.discardConfirm')}
        confirmLabel={t('webmail.compose.discard')}
        danger
        onCancel={() => setConfirmDiscard(false)}
        onConfirm={async () => {
          setConfirmDiscard(false);
          navigate(backHref);
        }}
      />
    </form>
  );
}

/**
 * Ficheros adjuntos. El webmail no publica sus topes (numero ni tamano): el servicio los
 * aplica al enviar y responde MESSAGE_TOO_LARGE, que la pantalla muestra.
 */
function AttachmentPicker({
  files,
  onChange,
  disabled,
}: {
  files: File[];
  onChange: (files: File[]) => void;
  disabled: boolean;
}) {
  const total = files.reduce((sum, file) => sum + file.size, 0);
  return (
    <div className="cf-field">
      <label className="cf-field__label" htmlFor="compose-files">
        {t('webmail.compose.attachments')}
      </label>
      <input
        id="compose-files"
        type="file"
        multiple
        className="cf-input cf-input--file"
        disabled={disabled}
        aria-describedby="compose-files-hint"
        onChange={(e) => {
          const picked = Array.from(e.target.files ?? []);
          if (picked.length) onChange([...files, ...picked]);
          e.target.value = '';
        }}
      />
      {files.length ? (
        <ul className="cf-wm-attachments">
          {files.map((file, index) => {
            const name = displayFilename(file.name, t('webmail.attachments.unnamed'));
            return (
              <li key={`${index}:${file.name}:${file.size}`} className="cf-wm-attachment">
                <IconPaperclip size={16} />
                <span className="cf-wm-attachment__name">{name}</span>
                <span className="cf-text-sm cf-text-muted">{formatBytes(file.size)}</span>
                <Button
                  size="sm"
                  variant="ghost"
                  iconOnly
                  icon={<IconX size={14} />}
                  disabled={disabled}
                  onClick={() => onChange(files.filter((_, i) => i !== index))}
                >
                  {t('webmail.compose.removeAttachment', { name })}
                </Button>
              </li>
            );
          })}
        </ul>
      ) : null}
      <span id="compose-files-hint" className="cf-field__hint">
        {files.length
          ? t('webmail.compose.attachmentsTotal', { n: files.length, size: formatBytes(total) })
          : t('webmail.compose.attachmentsHint')}
      </span>
    </div>
  );
}
