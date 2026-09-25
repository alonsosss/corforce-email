import { useEffect, useRef, useState, type FormEvent } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import {
  FOLDER_ROLES,
  webmailApi,
  type ComposeInput,
  type MailMessage,
  type QuickReply,
  type Signature,
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
  type ChipsInputProps,
} from '@/design/components';
import {
  IconChevronLeft,
  IconChevronUp,
  IconClock,
  IconMaximize,
  IconMinimize,
  IconMinus,
  IconSend,
  IconTrash,
  IconUndo,
  IconX,
} from '@/design/icons';
import { getLocale, t, type MessageKey } from '@/i18n';
import { paths } from '@/paths';
import { mailboxSignature, senderIdentities, webmailMeta } from '@/webmail/catalogs';
import { useWebmailStore } from '@/webmail/store';
import { AddressBookPicker } from './AddressBookPicker';
import { AttachmentPicker } from './AttachmentPicker';
import type { LargeFile } from '@/api/largeFiles';
import { LargeFilePicker } from './LargeFilePicker';
import { insertLargeFileLink, largeFileLinkHtml, largeFileLinkText } from './largeFiles';
import { ComposeAssistant } from './assistant/ComposeAssistant';
import {
  buildDraft,
  composeErrorMessage,
  composeProblems,
  EMPTY_DRAFT,
  forwardableParts,
  htmlToPlainText,
  identityLabel,
  initialBody,
  normalizeRecipient,
  parseComposeMode,
  pickSender,
  sendSignature,
  type ComposeMode,
  type DraftSeed,
  type ServerAttachments,
} from './compose';
import { folderWithRole } from './folders';
import { parsePositiveInt } from './format';
import { useRecipientSuggestions } from './recipients';
import { RichEditor, type RichEditorHandle } from './RichEditor';
import { htmlHasContent, textToHtml } from './richText';
import { namesOf, quickReplyValues, resolveQuickReply } from './quickReplies';
import { QuickReplyPicker } from './QuickReplyPicker';
import { followUpChoices } from './snooze';
import { formatScheduled } from './schedule';
import { ScheduleDialog } from './ScheduleDialog';
import { useComposeWindow } from './composeWindow';
import { embedInlineImages, loadInlineImages, referencedInlineParts } from './inlineImages';
import { useWebmailOutlet } from './webmailContext';

const TITLES: Record<ComposeMode | 'new', MessageKey> = {
  new: 'webmail.compose.title.new',
  reply: 'webmail.compose.title.reply',
  replyAll: 'webmail.compose.title.replyAll',
  forward: 'webmail.compose.title.forward',
  draft: 'webmail.compose.title.draft',
};

/** Plazo para deshacer un envio: hasta que vence no sale ninguna peticion. */
export const UNDO_SEND_MS = 10_000;
/** Pausa de escritura tras la que se guarda el borrador solo. */
export const AUTOSAVE_DELAY_MS = 5_000;

/**
 * Las imagenes de un borrador llegan como URL de sus partes, que el editor no conserva: se
 * incrustan como data: y al guardar o enviar el servicio las vuelve a convertir en cid:.
 */
async function withDraftImages(message: MailMessage, signal: AbortSignal): Promise<MailMessage> {
  const images = await loadInlineImages(message, referencedInlineParts(message), signal);
  return images.size ? { ...message, html: embedInlineImages(message.html, images) } : message;
}

export default function ComposePage() {
  const [params] = useSearchParams();
  const rawMode = params.get('mode');
  const mode = parseComposeMode(rawMode);
  const folder = params.get('folder');
  const uid = parsePositiveInt(params.get('uid'));
  const toParam = params.get('to');
  const username = useWebmailStore((s) => s.session?.username ?? '');
  // La firma no bloquea redactar: si no se puede leer, se abre sin ella.
  const signature = useResource(mailboxSignature);

  // Los adjuntos del original no se descargan: el servicio los toma del buzon al enviar o
  // guardar (source_folder, source_uid, source_parts) y los analiza como cualquier otro.
  const source = useQuery(
    (signal) =>
      mode && folder && uid
        ? webmailApi
            .message(folder, uid, { peek: true }, signal)
            .then((message) => (mode === 'draft' ? withDraftImages(message, signal) : message))
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
  if ((mode && !source.data) || (!signature.data && !signature.error)) {
    return (
      <div className="cf-wm-compose">
        <Skeleton lines={10} />
      </div>
    );
  }

  const direct = toParam ? normalizeRecipient(toParam) : null;
  const seed =
    mode && source.data
      ? buildDraft(mode, source.data, username)
      : { ...EMPTY_DRAFT, to: direct ? [direct] : [] };
  const backHref =
    folder && uid
      ? paths.webmailView({ folder, uid: mode === 'draft' ? undefined : uid })
      : paths.webmail;
  return (
    <ComposeForm
      key={`${mode ?? 'new'}:${folder ?? ''}:${uid ?? ''}:${direct ?? ''}`}
      mode={mode}
      seed={seed}
      signature={signature.data}
      backHref={backHref}
      recipientNames={mode && source.data ? namesOf(source.data) : undefined}
    />
  );
}

/** Clave de idempotencia del envio y el contenido para el que se genero. */
interface SendAttempt {
  key: string;
  signature: string;
}

type AutosaveState =
  { state: 'idle' } | { state: 'saving' } | { state: 'saved'; at: Date } | { state: 'error' };

function RecipientInput(
  props: Omit<ChipsInputProps, 'suggestions' | 'onQueryChange' | 'suggestionsLabel'>,
) {
  const [query, setQuery] = useState('');
  const suggestions = useRecipientSuggestions(query);
  return (
    <ChipsInput
      {...props}
      suggestions={suggestions}
      onQueryChange={setQuery}
      suggestionsLabel={t('webmail.suggest.label')}
    />
  );
}

function ComposeForm({
  mode,
  seed,
  signature,
  backHref,
  recipientNames,
}: {
  mode: ComposeMode | null;
  seed: DraftSeed;
  signature: Signature | null;
  backHref: string;
  /** Nombres visibles del mensaje al que se responde: resuelven {nombre} de una respuesta rapida. */
  recipientNames?: Readonly<Record<string, string>>;
}) {
  const toast = useToast();
  const navigate = useNavigate();
  const { folders, reloadFolders } = useWebmailOutlet();
  const refreshSession = useWebmailStore((s) => s.refresh);
  const meta = useResource(webmailMeta);
  const identities = useResource(senderIdentities);
  const [initial] = useState(() => initialBody(seed, mode, signature));
  const [chosenFrom, setChosenFrom] = useState<string | null>(null);
  const [to, setTo] = useState(seed.to);
  const [cc, setCc] = useState(seed.cc);
  const [bcc, setBcc] = useState(seed.bcc);
  const [showBook, setShowBook] = useState(false);
  const [showCopies, setShowCopies] = useState(seed.cc.length > 0 || seed.bcc.length > 0);
  const [subject, setSubject] = useState(seed.subject);
  const [format, setFormat] = useState<'html' | 'text'>('html');
  const [html, setHtml] = useState(initial.html);
  const [text, setText] = useState(initial.text);
  // El editor no es controlado: cambiar de modo lo vuelve a montar con el contenido convertido.
  const [editorKey, setEditorKey] = useState(0);
  const [files, setFiles] = useState<File[]>([]);
  const [server, setServer] = useState<ServerAttachments | undefined>(seed.source);
  const [draftUid, setDraftUid] = useState(seed.draftUid);
  // De donde sale el borrador: del enlace (se sigue uno), de un guardado a mano o de uno automatico.
  const [draftOrigin, setDraftOrigin] = useState<'none' | 'seed' | 'manual' | 'auto'>(
    seed.draftUid ? 'seed' : 'none',
  );
  const [recipientError, setRecipientError] = useState<string | null>(null);
  const [version, setVersion] = useState(0);
  const [savedVersion, setSavedVersion] = useState(0);
  const [autosave, setAutosave] = useState<AutosaveState>({ state: 'idle' });
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const [uncertain, setUncertain] = useState(false);
  const [scheduling, setScheduling] = useState(false);
  const [waiting, setWaiting] = useState(false);
  // Seguimiento: dias sin respuesta tras los que el mensaje vuelve a la entrada (0, sin aviso).
  const [followUpDays, setFollowUpDays] = useState(0);
  const mailboxName = useWebmailStore((s) => s.session?.display_name ?? '');
  const mailboxAddress = useWebmailStore((s) => s.session?.username ?? '');
  const attempt = useRef<SendAttempt | null>(null);
  const bodyRef = useRef<HTMLTextAreaElement>(null);
  const editorRef = useRef<RichEditorHandle>(null);
  const mounted = useRef(true);
  const pending = useRef<{ timer: number; toastId: number; flush: () => void } | null>(null);
  const dirty = version !== savedVersion;
  const windowed = useComposeWindow();
  // Salir a proposito (enviar, programar, descartar) no deja borrador; salir de otro modo, si.
  const closing = useRef(false);
  const saveOnLeave = useRef<() => void>(() => undefined);

  useEffect(() => {
    // Al responder se escribe encima de la cita; en lo demas se empieza por el destinatario.
    if (seed.inReplyTo) {
      if (bodyRef.current) {
        bodyRef.current.focus();
        bodyRef.current.setSelectionRange(0, 0);
      } else {
        editorRef.current?.focus();
      }
    } else {
      document.getElementById('compose-to')?.focus();
    }
    // Solo al abrir.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      // Salir de la redaccion dentro de la aplicacion no cancela un envio en espera: sale ya.
      pending.current?.flush();
      saveOnLeave.current();
    };
  }, []);

  useEffect(() => {
    if (!waiting) return;
    // Cerrar la pestana en el plazo de deshacer cancela el envio: el navegador lo pregunta.
    const onBeforeUnload = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      e.returnValue = '';
    };
    window.addEventListener('beforeunload', onBeforeUnload);
    return () => window.removeEventListener('beforeunload', onBeforeUnload);
  }, [waiting]);

  const senders = identities.data ?? [];
  const from = chosenFrom ?? pickSender(senders, seed.fromCandidates ?? []);
  const serverParts = server?.parts ?? [];
  const limits = meta.data?.limits ?? null;
  const body = format === 'html' ? html : text;
  const problems = composeProblems(
    { to, cc, bcc, subject: subject.trim(), text: body, files, serverParts },
    limits,
  );
  const blocked = Boolean(problems.recipients || problems.subject || problems.attachments);
  const hasContent =
    to.length + cc.length + bcc.length > 0 ||
    subject.trim() !== '' ||
    files.length > 0 ||
    (format === 'html' ? htmlHasContent(html) : text.trim() !== '');

  const edit =
    <T,>(setter: (value: T) => void) =>
    (value: T) => {
      setter(value);
      setVersion((v) => v + 1);
    };

  const input = (): ComposeInput => ({
    from: from || undefined,
    to,
    cc,
    bcc,
    subject: subject.trim(),
    // Con formato, la parte de texto la genera el servicio a partir del HTML saneado.
    text: format === 'html' ? '' : text,
    html: format === 'html' && htmlHasContent(html) ? html : undefined,
    inReplyTo: seed.inReplyTo,
    attachments: files,
    source: server?.parts.length
      ? { folder: server.folder, uid: server.uid, parts: server.parts.map((p) => p.part) }
      : undefined,
  });

  const leave = () => {
    closing.current = true;
    reloadFolders();
    void refreshSession();
    if (mounted.current) navigate(backHref, { replace: true });
  };

  // La clave de idempotencia se conserva mientras se reintenta el mismo contenido: un
  // reintento tras un corte no entrega el mensaje dos veces. Otro contenido, o reenviar a
  // proposito un envio en duda, estrena clave.
  const send = useAction(async (forceNewKey: boolean) => {
    const payload = input();
    const fingerprint = sendSignature(payload, draftUid);
    let current = attempt.current;
    if (forceNewKey || !current || current.signature !== fingerprint) {
      current = { key: crypto.randomUUID(), signature: fingerprint };
      attempt.current = current;
    }
    setUncertain(false);
    try {
      const result = await webmailApi.send(payload, {
        idempotencyKey: current.key,
        replaceUid: draftUid,
        followUpDays: followUpDays || undefined,
      });
      if (result.follow_up_error) toast.error(t('webmail.followUp.failed'));
      toast.success(t(result.replayed ? 'webmail.compose.alreadySent' : 'webmail.compose.sent'));
      if (!result.saved_to_sent) toast.info(t('webmail.compose.notSavedToSent'));
      if (draftUid && !result.draft_removed) toast.info(t('webmail.compose.draftKept'));
    } catch (err) {
      if (errorCode(err) === ERROR_CODES.DELIVERY_UNCERTAIN) setUncertain(true);
      if (!mounted.current) toast.error(composeErrorMessage(err));
      throw err;
    }
    leave();
  });

  const undo = () => {
    const current = pending.current;
    if (!current) return;
    window.clearTimeout(current.timer);
    toast.dismiss(current.toastId);
    pending.current = null;
    setWaiting(false);
    toast.info(t('webmail.compose.sendUndone'));
  };

  const queueSend = (forceNewKey: boolean) => {
    const fire = () => {
      pending.current = null;
      if (mounted.current) setWaiting(false);
      void send.run(forceNewKey);
    };
    const timer = window.setTimeout(fire, UNDO_SEND_MS);
    const toastId = toast.show({
      kind: 'info',
      message: t('webmail.compose.sendingSoon', { s: Math.round(UNDO_SEND_MS / 1000) }),
      action: { label: t('webmail.compose.undo'), onAction: undo },
      durationMs: UNDO_SEND_MS,
    });
    pending.current = {
      timer,
      toastId,
      flush: () => {
        window.clearTimeout(timer);
        toast.dismiss(toastId);
        fire();
      },
    };
    setWaiting(true);
  };

  // El borrador guardado ya lleva los adjuntos que se subieron con el: desde aqui salen de
  // el en el servidor. Los anadidos mientras se guardaba siguen pendientes de subir.
  const rebaseOnDraft = async (uid: number, uploaded: readonly File[]) => {
    const drafts = folders.data ? folderWithRole(folders.data, FOLDER_ROLES.drafts) : undefined;
    if (!uid || !drafts) return;
    try {
      const saved = await webmailApi.message(drafts.name, uid, { peek: true });
      const parts = forwardableParts(saved);
      setServer(parts.length ? { folder: drafts.name, uid, parts } : undefined);
      setFiles((current) => current.filter((file) => !uploaded.includes(file)));
    } catch {
      // Se conserva lo que habia; si el origen era el borrador reemplazado, el envio lo dira.
    }
  };

  const persistDraft = async (quiet: boolean) => {
    const captured = version;
    const uploaded = files;
    const { uid } = await webmailApi.saveDraft(input(), draftUid);
    setDraftUid(uid);
    if (draftOrigin === 'none') setDraftOrigin(quiet ? 'auto' : 'manual');
    setSavedVersion(captured);
    setAutosave({ state: 'saved', at: new Date() });
    if (!quiet) toast.success(t('webmail.compose.draftSaved'));
    reloadFolders();
    void refreshSession();
    await rebaseOnDraft(uid, uploaded);
  };

  const save = useAction(() => persistDraft(false));

  const autosaving = autosave.state === 'saving';
  useEffect(() => {
    if (!dirty || !hasContent || blocked || waiting || send.busy || save.busy || autosaving) {
      return;
    }
    const timer = window.setTimeout(() => {
      setAutosave({ state: 'saving' });
      persistDraft(true).catch(() => setAutosave({ state: 'error' }));
    }, AUTOSAVE_DELAY_MS);
    return () => window.clearTimeout(timer);
    // version resume cualquier cambio del contenido.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [version, dirty, hasContent, blocked, waiting, send.busy, save.busy, autosaving]);

  // En la ventana flotante, cerrarla o navegar por el buzon de fondo guarda lo escrito como
  // borrador en lugar de perderlo (lo pendiente de guardar solo en los ultimos segundos).
  saveOnLeave.current = () => {
    if (!windowed || closing.current || !dirty || !hasContent || blocked) return;
    // Con la sesion cerrada o caducada no hay donde guardar: la peticion solo fallaria.
    if (useWebmailStore.getState().status !== 'authenticated') return;
    if (waiting || send.busy || save.busy || autosaving) return;
    webmailApi.saveDraft(input(), draftUid).then(
      () => {
        toast.info(t('webmail.composer.savedOnLeave'));
        reloadFolders();
      },
      () => toast.error(t('webmail.composer.saveOnLeaveFailed')),
    );
  };

  const ready = (): boolean => {
    if (to.length + cc.length + bcc.length === 0) {
      setRecipientError(t('webmail.compose.noRecipients'));
      return false;
    }
    setRecipientError(null);
    return !blocked;
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (waiting || autosaving || !ready()) return;
    save.clearError();
    send.clearError();
    queueSend(false);
  };

  // El enlace de un fichero grande va al cuerpo; si era un adjunto, sale de los adjuntos.
  const addLargeFileLink = (shared: LargeFile, source?: File) => {
    const quoted = mode !== null && mode !== 'draft';
    if (source) setFiles((current) => current.filter((file) => file !== source));
    if (format === 'html') {
      setHtml((current) => insertLargeFileLink(current, largeFileLinkHtml(shared), 'html', quoted));
      setEditorKey((k) => k + 1);
    } else {
      setText((current) => insertLargeFileLink(current, largeFileLinkText(shared), 'text', quoted));
    }
    setVersion((v) => v + 1);
  };

  const switchFormat = () => {
    if (format === 'html') {
      setText(htmlToPlainText(html));
      setFormat('text');
    } else {
      setHtml(textToHtml(text));
      setFormat('html');
      setEditorKey((k) => k + 1);
    }
    setVersion((v) => v + 1);
  };

  // La respuesta rapida entra al principio del cuerpo, encima de la cita y la firma, con sus
  // variables resueltas para el primer destinatario.
  const insertQuickReply = (reply: QuickReply) => {
    const values = quickReplyValues({
      recipient: to[0] ?? cc[0] ?? bcc[0],
      names: recipientNames,
      mailbox: { email: mailboxAddress, name: mailboxName },
      now: new Date(),
    });
    if (format === 'html') {
      const piece = resolveQuickReply(reply.html || textToHtml(reply.text), values, true);
      edit(setHtml)(piece + html);
      setEditorKey((k) => k + 1);
    } else {
      const piece = resolveQuickReply(reply.text, values, false);
      edit(setText)(text ? `${piece}\n\n${text}` : piece);
    }
  };

  // Texto propuesto por el asistente: una respuesta va delante (encima de la cita); un cambio de tono
  // sustituye el cuerpo. El editor no es controlado, asi que se vuelve a montar con el contenido nuevo.
  const insertAssistantText = (value: string) => {
    if (format === 'html') {
      setHtml((current) => textToHtml(value) + current);
      setEditorKey((k) => k + 1);
    } else {
      setText((current) => (current ? `${value}\n\n${current}` : value));
    }
    setVersion((v) => v + 1);
  };
  const replaceWithAssistantText = (value: string) => {
    if (format === 'html') {
      setHtml(textToHtml(value));
      setEditorKey((k) => k + 1);
    } else {
      setText(value);
    }
    setVersion((v) => v + 1);
  };

  const discard = async () => {
    closing.current = true;
    // Un borrador que solo existe porque se guardo solo se retira con lo descartado.
    const drafts = folders.data ? folderWithRole(folders.data, FOLDER_ROLES.drafts) : undefined;
    if (draftOrigin === 'auto' && draftUid && drafts) {
      await webmailApi.remove(drafts.name, draftUid);
      reloadFolders();
    }
    setConfirmDiscard(false);
    navigate(backHref);
  };

  const busy = send.busy || save.busy || waiting;
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
      onSubmit={submit}
      onKeyDown={(e) => {
        // Enter en un campo de una linea no envia: enviar es siempre un gesto explicito.
        const target = e.target as HTMLInputElement;
        if (e.key === 'Enter' && target.tagName === 'INPUT' && target.form === e.currentTarget) {
          e.preventDefault();
        }
      }}
      noValidate
      aria-labelledby="wm-compose-title"
    >
      <div className="cf-wm-compose__head">
        {windowed ? null : (
          <Link to={backHref} className="cf-btn cf-btn--ghost cf-btn--sm">
            <IconChevronLeft size={16} />
            {t('webmail.reader.back')}
          </Link>
        )}
        <h1 id="wm-compose-title" className="cf-wm-compose__title">
          {windowed ? (
            <button
              type="button"
              className="cf-wm-compose__titlebutton"
              aria-expanded={windowed.size !== 'minimized'}
              onClick={() =>
                windowed.setSize(windowed.size === 'minimized' ? 'normal' : 'minimized')
              }
            >
              {subject.trim() || t(TITLES[mode ?? 'new'])}
            </button>
          ) : (
            t(TITLES[mode ?? 'new'])
          )}
        </h1>
        <AutosaveStatus state={autosave} dirty={dirty} />
        {windowed ? (
          <div className="cf-wm-compose__window">
            <Button
              size="sm"
              variant="ghost"
              iconOnly
              icon={
                windowed.size === 'minimized' ? (
                  <IconChevronUp size={16} />
                ) : (
                  <IconMinus size={16} />
                )
              }
              onClick={() =>
                windowed.setSize(windowed.size === 'minimized' ? 'normal' : 'minimized')
              }
            >
              {t(
                windowed.size === 'minimized'
                  ? 'webmail.composer.restore'
                  : 'webmail.composer.minimize',
              )}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              iconOnly
              className="cf-wm-compose__expand"
              icon={
                windowed.size === 'expanded' ? (
                  <IconMinimize size={16} />
                ) : (
                  <IconMaximize size={16} />
                )
              }
              onClick={() => windowed.setSize(windowed.size === 'expanded' ? 'normal' : 'expanded')}
            >
              {t(
                windowed.size === 'expanded'
                  ? 'webmail.composer.collapse'
                  : 'webmail.composer.expand',
              )}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              iconOnly
              icon={<IconX size={16} />}
              onClick={() => navigate(backHref)}
            >
              {t('webmail.composer.close')}
            </Button>
          </div>
        ) : null}
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
        <RecipientInput
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
            <RecipientInput id="compose-cc" values={cc} onChange={edit(setCc)} {...chips} />
          </FormField>
          <FormField label={t('webmail.header.bcc')} htmlFor="compose-bcc">
            <RecipientInput id="compose-bcc" values={bcc} onChange={edit(setBcc)} {...chips} />
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
      <div className="cf-field">
        <div className="cf-wm-compose__bodyhead">
          {format === 'html' ? (
            <span className="cf-field__label" id="compose-body-label">
              {t('webmail.compose.body')}
            </span>
          ) : (
            <label className="cf-field__label" htmlFor="compose-body" id="compose-body-label">
              {t('webmail.compose.body')}
            </label>
          )}
          <QuickReplyPicker disabled={busy} onPick={insertQuickReply} />
          <Button size="sm" variant="ghost" disabled={busy} onClick={switchFormat}>
            {t(format === 'html' ? 'webmail.compose.toPlain' : 'webmail.compose.toRich')}
          </Button>
        </div>
        {format === 'html' ? (
          <RichEditor
            key={editorKey}
            ref={editorRef}
            id="compose-body"
            labelledBy="compose-body-label"
            initialHtml={html}
            allowImages
            maxImageBytes={
              limits ? Math.min(limits.max_download_bytes, limits.max_message_bytes) : null
            }
            minHeight="18rem"
            onChange={edit(setHtml)}
            disabled={busy}
          />
        ) : (
          <Textarea
            ref={bodyRef}
            id="compose-body"
            rows={14}
            value={text}
            onChange={(e) => edit(setText)(e.target.value)}
            disabled={busy}
          />
        )}
      </div>
      <ComposeAssistant
        bodyText={() => (format === 'html' ? htmlToPlainText(html) : text)}
        replyTo={seed.inReplyTo}
        disabled={busy}
        onInsert={insertAssistantText}
        onReplace={replaceWithAssistantText}
      />
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
      <FormField label={t('webmail.followUp.label')} htmlFor="compose-follow-up">
        <Select
          id="compose-follow-up"
          options={[
            { value: '0', label: t('webmail.followUp.none') },
            ...followUpChoices(limits?.max_reminder_days ?? null).map((days) => ({
              value: String(days),
              label:
                days === 1 ? t('webmail.followUp.oneDay') : t('webmail.followUp.days', { n: days }),
            })),
          ]}
          value={String(followUpDays)}
          onChange={(e) => setFollowUpDays(Number(e.target.value))}
          disabled={busy}
        />
      </FormField>
      <LargeFilePicker
        suggest={problems.attachments ? files : []}
        disabled={busy}
        onShared={addLargeFileLink}
      />
      {waiting ? (
        <div className="cf-wm-undo" role="status">
          <span>{t('webmail.compose.sendingSoon', { s: Math.round(UNDO_SEND_MS / 1000) })}</span>
          <Button size="sm" icon={<IconUndo size={16} />} onClick={undo}>
            {t('webmail.compose.undo')}
          </Button>
        </div>
      ) : uncertain ? (
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
          type="submit"
          variant="primary"
          icon={<IconSend size={16} />}
          loading={send.busy}
          disabled={save.busy || waiting || autosaving}
        >
          {t('webmail.compose.send')}
        </Button>
        <Button
          variant="ghost"
          iconOnly
          icon={<IconClock size={18} />}
          disabled={busy}
          onClick={() => {
            if (ready()) setScheduling(true);
          }}
        >
          {t('webmail.compose.sendLater')}
        </Button>
        <Button
          variant="ghost"
          loading={save.busy}
          disabled={send.busy || waiting || blocked || autosaving}
          onClick={() => {
            send.clearError();
            setUncertain(false);
            void save.run();
          }}
        >
          {t('webmail.compose.saveDraft')}
        </Button>
        <Button
          variant="ghost"
          iconOnly
          className="cf-wm-compose__discard"
          icon={<IconTrash size={18} />}
          disabled={busy || autosaving}
          onClick={() =>
            dirty || draftOrigin === 'auto' ? setConfirmDiscard(true) : navigate(backHref)
          }
        >
          {t('webmail.compose.discard')}
        </Button>
      </div>
      {scheduling ? (
        <ScheduleDialog
          title={t('webmail.schedule.title')}
          confirmLabel={t('webmail.schedule.confirm')}
          onClose={() => setScheduling(false)}
          onConfirm={async (sendAt) => {
            const scheduled = await webmailApi.schedule(input(), sendAt, {
              idempotencyKey: crypto.randomUUID(),
              replaceUid: draftUid,
              followUpDays: followUpDays || undefined,
            });
            if (scheduled.follow_up_error) toast.error(t('webmail.followUp.failed'));
            toast.success(t('webmail.schedule.done', { when: formatScheduled(scheduled.send_at) }));
            setScheduling(false);
            leave();
          }}
        />
      ) : null}
      <ConfirmDialog
        open={confirmDiscard}
        title={t('webmail.compose.discardTitle')}
        message={t(
          draftOrigin === 'auto'
            ? 'webmail.compose.discardAutosaved'
            : 'webmail.compose.discardConfirm',
        )}
        confirmLabel={t('webmail.compose.discard')}
        danger
        onCancel={() => setConfirmDiscard(false)}
        onConfirm={discard}
      />
    </form>
  );
}

function AutosaveStatus({ state, dirty }: { state: AutosaveState; dirty: boolean }) {
  let label: string | null = null;
  if (state.state === 'saving') label = t('webmail.compose.autosaving');
  else if (state.state === 'error') label = t('webmail.compose.autosaveFailed');
  else if (state.state === 'saved' && !dirty) {
    label = t('webmail.compose.autosaved', {
      time: new Intl.DateTimeFormat(getLocale(), { hour: '2-digit', minute: '2-digit' }).format(
        state.at,
      ),
    });
  }
  return (
    <span className="cf-wm-compose__status cf-text-sm cf-text-secondary" role="status">
      {label}
    </span>
  );
}
