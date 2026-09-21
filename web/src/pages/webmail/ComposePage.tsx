import { useEffect, useRef, useState, type FormEvent } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import {
  FOLDER_ROLES,
  webmailApi,
  type ComposeInput,
  type MessagePart,
  type WebmailMeta,
} from '@/api/webmail';
import { ERROR_CODES, errorCode } from '@/api/errors';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import { useResource } from '@/hooks/useResource';
import {
  Button,
  ChipsInput,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  FormField,
  Input,
  Select,
  Skeleton,
  Textarea,
  useToast,
} from '@/design/components';
import { IconChevronLeft, IconPaperclip, IconSend, IconX } from '@/design/icons';
import { formatBytes } from '@/lib/quota';
import { t, type MessageKey } from '@/i18n';
import { paths } from '@/paths';
import { senderIdentities, webmailMeta } from '@/webmail/catalogs';
import { useWebmailStore } from '@/webmail/store';
import {
  buildDraft,
  composeErrorMessage,
  composeProblems,
  EMPTY_DRAFT,
  forwardableParts,
  identityLabel,
  normalizeRecipient,
  parseComposeMode,
  pickSender,
  sendSignature,
  type ComposeMode,
  type DraftSeed,
  type ServerAttachments,
} from './compose';
import { AddressBookPicker } from './AddressBookPicker';
import { folderWithRole } from './folders';
import { displayFilename, parsePositiveInt } from './format';
import { useWebmailOutlet } from './webmailContext';

const TITLES: Record<ComposeMode | 'new', MessageKey> = {
  new: 'webmail.compose.title.new',
  reply: 'webmail.compose.title.reply',
  replyAll: 'webmail.compose.title.replyAll',
  forward: 'webmail.compose.title.forward',
  draft: 'webmail.compose.title.draft',
};

export default function ComposePage() {
  const [params] = useSearchParams();
  const rawMode = params.get('mode');
  const mode = parseComposeMode(rawMode);
  const folder = params.get('folder');
  const uid = parsePositiveInt(params.get('uid'));
  const username = useWebmailStore((s) => s.session?.username ?? '');

  // Los adjuntos del original no se descargan: el servicio los toma del buzon al enviar o
  // guardar (source_folder, source_uid, source_parts) y los analiza como cualquier otro.
  const source = useQuery(
    (signal) =>
      mode && folder && uid
        ? webmailApi.message(folder, uid, { peek: true }, signal)
        : Promise.resolve(null),
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

  const seed = mode && source.data ? buildDraft(mode, source.data, username) : EMPTY_DRAFT;
  const backHref =
    folder && uid
      ? paths.webmailView({ folder, uid: mode === 'draft' ? undefined : uid })
      : paths.webmail;
  return (
    <ComposeForm
      key={`${mode ?? 'new'}:${folder ?? ''}:${uid ?? ''}`}
      mode={mode}
      seed={seed}
      backHref={backHref}
    />
  );
}

/** Clave de idempotencia del envio y el contenido para el que se genero. */
interface SendAttempt {
  key: string;
  signature: string;
}

function ComposeForm({
  mode,
  seed,
  backHref,
}: {
  mode: ComposeMode | null;
  seed: DraftSeed;
  backHref: string;
}) {
  const toast = useToast();
  const navigate = useNavigate();
  const { folders, reloadFolders } = useWebmailOutlet();
  const refreshSession = useWebmailStore((s) => s.refresh);
  const meta = useResource(webmailMeta);
  const identities = useResource(senderIdentities);
  const [chosenFrom, setChosenFrom] = useState<string | null>(null);
  const [to, setTo] = useState(seed.to);
  const [cc, setCc] = useState(seed.cc);
  const [bcc, setBcc] = useState(seed.bcc);
  const [showBook, setShowBook] = useState(false);
  const [showCopies, setShowCopies] = useState(seed.cc.length > 0 || seed.bcc.length > 0);
  const [subject, setSubject] = useState(seed.subject);
  const [text, setText] = useState(seed.text);
  const [files, setFiles] = useState<File[]>([]);
  const [server, setServer] = useState<ServerAttachments | undefined>(seed.source);
  const [draftUid, setDraftUid] = useState(seed.draftUid);
  const [recipientError, setRecipientError] = useState<string | null>(null);
  const [dirty, setDirty] = useState(false);
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const [uncertain, setUncertain] = useState(false);
  const attempt = useRef<SendAttempt | null>(null);
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

  const senders = identities.data ?? [];
  const from = chosenFrom ?? pickSender(senders, seed.fromCandidates ?? []);
  const serverParts = server?.parts ?? [];
  const limits = meta.data?.limits ?? null;
  const problems = composeProblems(
    { to, cc, bcc, subject: subject.trim(), text, files, serverParts },
    limits,
  );
  const blocked = Boolean(problems.recipients || problems.subject || problems.attachments);

  const edit =
    <T,>(setter: (value: T) => void) =>
    (value: T) => {
      setter(value);
      setDirty(true);
    };

  const input = (): ComposeInput => ({
    from: from || undefined,
    to,
    cc,
    bcc,
    subject: subject.trim(),
    text,
    inReplyTo: seed.inReplyTo,
    attachments: files,
    source: server?.parts.length
      ? { folder: server.folder, uid: server.uid, parts: server.parts.map((p) => p.part) }
      : undefined,
  });

  // La clave de idempotencia se conserva mientras se reintenta el mismo contenido: un
  // reintento tras un corte no entrega el mensaje dos veces. Otro contenido, o reenviar a
  // proposito un envio en duda, estrena clave.
  const send = useAction(async (forceNewKey: boolean) => {
    const payload = input();
    const signature = sendSignature(payload, draftUid);
    let current = attempt.current;
    if (forceNewKey || !current || current.signature !== signature) {
      current = { key: crypto.randomUUID(), signature };
      attempt.current = current;
    }
    setUncertain(false);
    try {
      const result = await webmailApi.send(payload, {
        idempotencyKey: current.key,
        replaceUid: draftUid,
      });
      toast.success(t(result.replayed ? 'webmail.compose.alreadySent' : 'webmail.compose.sent'));
      if (!result.saved_to_sent) toast.info(t('webmail.compose.notSavedToSent'));
      if (draftUid && !result.draft_removed) toast.info(t('webmail.compose.draftKept'));
    } catch (err) {
      if (errorCode(err) === ERROR_CODES.DELIVERY_UNCERTAIN) setUncertain(true);
      throw err;
    }
    reloadFolders();
    void refreshSession();
    navigate(backHref, { replace: true });
  });

  // El borrador guardado ya lleva todos los adjuntos: desde aqui salen de el en el servidor
  // y no se vuelven a subir. El borrador anterior, y con el sus partes, ya no existe.
  const rebaseOnDraft = async (uid: number) => {
    const drafts = folders.data ? folderWithRole(folders.data, FOLDER_ROLES.drafts) : undefined;
    if (!uid || !drafts) return;
    try {
      const saved = await webmailApi.message(drafts.name, uid, { peek: true });
      const parts = forwardableParts(saved);
      setServer(parts.length ? { folder: drafts.name, uid, parts } : undefined);
      setFiles([]);
    } catch {
      // Se conserva lo que habia; si el origen era el borrador reemplazado, el envio lo dira.
    }
  };

  const save = useAction(async () => {
    const { uid } = await webmailApi.saveDraft(input(), draftUid);
    setDraftUid(uid);
    setDirty(false);
    toast.success(t('webmail.compose.draftSaved'));
    reloadFolders();
    void refreshSession();
    await rebaseOnDraft(uid);
  });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (to.length + cc.length + bcc.length === 0) {
      setRecipientError(t('webmail.compose.noRecipients'));
      return;
    }
    setRecipientError(null);
    if (blocked) return;
    save.clearError();
    await send.run(false);
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
      {senders.length > 1 ? (
        <FormField label={t('webmail.header.from')} htmlFor="compose-from">
          <Select
            id="compose-from"
            options={senders.map((identity) => ({
              value: identity.email,
              label: identityLabel(identity),
            }))}
            value={from}
            onChange={(e) => edit(setChosenFrom)(e.target.value)}
            disabled={busy}
          />
        </FormField>
      ) : null}
      {identities.error ? (
        <p className="cf-field__hint">{t('webmail.compose.identitiesUnavailable')}</p>
      ) : null}
      <FormField
        label={t('webmail.header.to')}
        htmlFor="compose-to"
        error={recipientError ?? problems.recipients ?? null}
      >
        <ChipsInput
          id="compose-to"
          values={to}
          onChange={(values) => {
            edit(setTo)(values);
            setRecipientError(null);
          }}
          invalid={Boolean(recipientError ?? problems.recipients)}
          {...chips}
        />
      </FormField>
      <div>
        <Button
          size="sm"
          variant="ghost"
          onClick={() => setShowBook((open) => !open)}
          aria-expanded={showBook}
        >
          {t('webmail.compose.addressBook')}
        </Button>
        {showBook ? (
          <AddressBookPicker
            chosen={[...to, ...cc, ...bcc]}
            onPick={(address) => {
              edit(setTo)([...to, address]);
              setRecipientError(null);
            }}
          />
        ) : null}
      </div>
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
      <FormField
        label={t('webmail.header.subject')}
        htmlFor="compose-subject"
        error={problems.subject ?? null}
      >
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
      <AttachmentPicker
        files={files}
        serverParts={serverParts}
        onFiles={edit(setFiles)}
        onServerParts={(parts) =>
          edit(setServer)(server && parts.length ? { ...server, parts } : undefined)
        }
        limits={limits}
        error={problems.attachments ?? null}
        disabled={busy}
      />
      {uncertain ? (
        <div className="cf-form__error" role="alert">
          <span>{t('error.code.DELIVERY_UNCERTAIN')}</span>{' '}
          <Button size="sm" disabled={busy} onClick={() => void send.run(true)}>
            {t('webmail.compose.sendAnyway')}
          </Button>
        </div>
      ) : error ? (
        <div className="cf-form__error" role="alert">
          {composeErrorMessage(error)}
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
          disabled={send.busy || blocked}
          onClick={() => {
            send.clearError();
            setUncertain(false);
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
 * Adjuntos: los del mensaje de origen, que el servicio toma del buzon, y los ficheros que
 * se suben. Numero y tamano maximos llegan en la meta del servicio.
 */
function AttachmentPicker({
  files,
  serverParts,
  onFiles,
  onServerParts,
  limits,
  error,
  disabled,
}: {
  files: File[];
  serverParts: readonly MessagePart[];
  onFiles: (files: File[]) => void;
  onServerParts: (parts: MessagePart[]) => void;
  limits: WebmailMeta['limits'] | null;
  error: string | null;
  disabled: boolean;
}) {
  const count = files.length + serverParts.length;
  const total =
    files.reduce((sum, file) => sum + file.size, 0) +
    serverParts.reduce((sum, part) => sum + part.size, 0);
  const unnamed = t('webmail.attachments.unnamed');
  const hint = [
    count
      ? t('webmail.compose.attachmentsTotal', { n: count, size: formatBytes(total) })
      : t('webmail.compose.attachmentsHint'),
    limits
      ? t('webmail.compose.attachmentsLimits', {
          n: limits.max_attachments,
          size: formatBytes(limits.max_message_bytes),
        })
      : '',
  ]
    .filter(Boolean)
    .join(' ');
  const item = (key: string, name: string, size: number, onRemove: () => void) => (
    <li key={key} className="cf-wm-attachment">
      <IconPaperclip size={16} />
      <span className="cf-wm-attachment__name">{name}</span>
      <span className="cf-text-sm cf-text-muted">{formatBytes(size)}</span>
      <Button
        size="sm"
        variant="ghost"
        iconOnly
        icon={<IconX size={14} />}
        disabled={disabled}
        onClick={onRemove}
      >
        {t('webmail.compose.removeAttachment', { name })}
      </Button>
    </li>
  );
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
          if (picked.length) onFiles([...files, ...picked]);
          e.target.value = '';
        }}
      />
      {count ? (
        <ul className="cf-wm-attachments">
          {serverParts.map((part) =>
            item(`s:${part.part}`, displayFilename(part.filename, unnamed), part.size, () =>
              onServerParts(serverParts.filter((p) => p.part !== part.part)),
            ),
          )}
          {files.map((file, index) =>
            item(
              `f:${index}:${file.name}:${file.size}`,
              displayFilename(file.name, unnamed),
              file.size,
              () => onFiles(files.filter((_, i) => i !== index)),
            ),
          )}
        </ul>
      ) : null}
      <span id="compose-files-hint" className="cf-field__hint">
        {hint}
      </span>
      {error ? (
        <div className="cf-form__error" role="alert">
          {error}
        </div>
      ) : null}
    </div>
  );
}
