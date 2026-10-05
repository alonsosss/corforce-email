import { useContext, useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react';
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
  IconChevronUp,
  IconClock,
  IconExternal,
  IconMaximize,
  IconMinimize,
  IconMinus,
  IconPaperclip,
  IconQuote,
  IconSend,
  IconTrash,
  IconUndo,
  IconX,
} from '@/design/icons';
import { getLocale, t, type MessageKey } from '@/i18n';
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
  pickSender,
  sendSignature,
  withQuoted,
  type ComposeMode,
  type DraftSeed,
  type ServerAttachments,
} from './compose';
import { folderWithRole } from './folders';
import { useRecipientSuggestions } from './recipients';
import { RichEditor, type RichEditorHandle } from './RichEditor';
import { htmlHasContent, textToHtml } from './richText';
import { namesOf, quickReplyValues, resolveQuickReply } from './quickReplies';
import { QuickReplyPicker } from './QuickReplyPicker';
import { followUpChoices } from './snooze';
import { formatScheduled } from './schedule';
import { ScheduleDialog } from './ScheduleDialog';
import {
  ComposeControllerContext,
  ComposePristineContext,
  useComposeWindow,
  type ComposeRequest,
  type ComposeResume,
} from './composeWindow';
import { loadInlineImages, referencedInlineParts } from './inlineImages';
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

/** Mensaje de partida con sus imagenes en linea ya descargadas (URL de la parte -> data:). */
interface ComposeSource {
  message: MailMessage;
  inlineImages: Map<string, string>;
}

/**
 * Las imagenes en linea del original (borrador, respuesta o reenvio) llegan como URL de sus
 * partes, que el editor no conserva: buildDraft las incrusta como data: y al guardar o enviar
 * el servicio las vuelve a convertir en cid:.
 */
async function loadComposeSource(
  folder: string,
  uid: number,
  signal: AbortSignal,
): Promise<ComposeSource> {
  const message = await webmailApi.message(folder, uid, { peek: true }, signal);
  const refs = referencedInlineParts(message);
  const inlineImages = refs.size
    ? await loadInlineImages(message, refs, signal)
    : new Map<string, string>();
  return { message, inlineImages };
}

export interface ComposePageProps {
  request: ComposeRequest;
  /** Cierra la redaccion: tras enviar, programar o descartar, o cuando el usuario la cierra. */
  onClose: () => void;
  /**
   * Respuesta dentro del lector, debajo del mensaje, en lugar de la ventana flotante. Sus ids
   * llevan otro prefijo: puede coexistir con una redaccion abierta en la ventana.
   */
  inline?: boolean;
}

export default function ComposePage({ request, onClose, inline = false }: ComposePageProps) {
  const mode = request.kind === 'source' ? request.mode : null;
  const folder = request.kind === 'source' ? request.folder : null;
  const uid = request.kind === 'source' ? request.uid : null;
  const username = useWebmailStore((s) => s.session?.username ?? '');
  // La firma no bloquea redactar: si no se puede leer, se abre sin ella.
  const signature = useResource(mailboxSignature);

  // Los adjuntos del original no se descargan: el servicio los toma del buzon al enviar o
  // guardar (source_folder, source_uid, source_parts) y los analiza como cualquier otro.
  const source = useQuery(
    (signal) =>
      mode && folder && uid ? loadComposeSource(folder, uid, signal) : Promise.resolve(null),
    [mode, folder, uid],
  );

  // Una redaccion que se retoma ya trae lo escrito (con su firma): no se vuelve a construir.
  if (request.kind === 'resume') {
    return (
      <ComposeForm
        mode={request.mode}
        seed={request.seed}
        signature={null}
        assistantText={null}
        onClose={onClose}
        recipientNames={request.recipientNames}
        inline={inline}
        resume={request}
      />
    );
  }

  if (mode && source.error) {
    return (
      <div className="cf-wm-compose">
        <ComposeHead title={t(TITLES[mode])} titleId={composeIds(inline).title} onClose={onClose} />
        <ErrorState error={source.error} onRetry={source.reload} />
      </div>
    );
  }
  if ((mode && !source.data) || (!signature.data && !signature.error)) {
    return (
      <div className="cf-wm-compose">
        <ComposeHead
          title={t(TITLES[mode ?? 'new'])}
          titleId={composeIds(inline).title}
          onClose={onClose}
        />
        <Skeleton lines={10} />
      </div>
    );
  }

  const direct = request.kind === 'new' && request.to ? normalizeRecipient(request.to) : null;
  const seed =
    mode && source.data
      ? buildDraft(mode, source.data.message, username, source.data.inlineImages)
      : { ...EMPTY_DRAFT, to: direct ? [direct] : [] };
  return (
    <ComposeForm
      mode={mode}
      seed={seed}
      signature={signature.data}
      assistantText={request.assistantText}
      onClose={onClose}
      recipientNames={mode && source.data ? namesOf(source.data.message) : undefined}
      inline={inline}
    />
  );
}

/** Ids de los campos: la redaccion en linea y la de la ventana pueden estar abiertas a la vez. */
function composeIds(inline: boolean) {
  const prefix = inline ? 'inline-compose' : 'compose';
  return {
    title: `${prefix}-title`,
    from: `${prefix}-from`,
    to: `${prefix}-to`,
    cc: `${prefix}-cc`,
    bcc: `${prefix}-bcc`,
    subject: `${prefix}-subject`,
    body: `${prefix}-body`,
    bodyLabel: `${prefix}-body-label`,
    followUp: `${prefix}-follow-up`,
  };
}

/** Cabecera de la redaccion: titulo y, en la ventana flotante, minimizar, ampliar y cerrar. */
function ComposeHead({
  title,
  titleId,
  status,
  onClose,
  onPopOut,
  popOutDisabled = false,
}: {
  title: string;
  titleId: string;
  status?: ReactNode;
  onClose: () => void;
  /** Pasa la redaccion del lector a la ventana flotante con lo escrito. */
  onPopOut?: () => void;
  /** Mientras se envia o se guarda el borrador, lo escrito no puede cambiar de sitio. */
  popOutDisabled?: boolean;
}) {
  const windowed = useComposeWindow();
  const toggleMinimized = () =>
    windowed?.setSize(windowed.size === 'minimized' ? 'normal' : 'minimized');
  return (
    <div className="cf-wm-compose__head">
      <h1 id={titleId} className="cf-wm-compose__title">
        {windowed ? (
          <button
            type="button"
            className="cf-wm-compose__titlebutton"
            aria-expanded={windowed.size !== 'minimized'}
            onClick={toggleMinimized}
          >
            {title}
          </button>
        ) : (
          title
        )}
      </h1>
      {status}
      <div className="cf-wm-compose__window">
        {windowed ? (
          <>
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
              onClick={toggleMinimized}
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
          </>
        ) : null}
        {onPopOut ? (
          <Button
            size="sm"
            variant="ghost"
            iconOnly
            title={t('webmail.composer.popOut')}
            icon={<IconExternal size={16} />}
            disabled={popOutDisabled}
            onClick={onPopOut}
          >
            {t('webmail.composer.popOut')}
          </Button>
        ) : null}
        <Button size="sm" variant="ghost" iconOnly icon={<IconX size={16} />} onClick={onClose}>
          {t('webmail.composer.close')}
        </Button>
      </div>
    </div>
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
  assistantText,
  onClose,
  recipientNames,
  inline,
  resume,
}: {
  mode: ComposeMode | null;
  seed: DraftSeed;
  signature: Signature | null;
  /** Texto propuesto por el asistente del lector: se inserta al abrir. */
  assistantText: string | null;
  onClose: () => void;
  /** Nombres visibles del mensaje al que se responde: resuelven {nombre} de una respuesta rapida. */
  recipientNames?: Readonly<Record<string, string>>;
  inline: boolean;
  /** Redaccion en curso que se retoma: lo escrito, sus adjuntos locales y su formato. */
  resume?: ComposeResume;
}) {
  const toast = useToast();
  const ids = composeIds(inline);
  const composer = useContext(ComposeControllerContext);
  const { folders, reloadFolders } = useWebmailOutlet();
  const reportPristine = useContext(ComposePristineContext);
  const refreshSession = useWebmailStore((s) => s.refresh);
  const meta = useResource(webmailMeta);
  const identities = useResource(senderIdentities);
  const [initial] = useState(() =>
    resume
      ? { text: seed.text, html: seed.html ?? textToHtml(seed.text) }
      : initialBody(seed, mode, signature),
  );
  const [chosenFrom, setChosenFrom] = useState<string | null>(null);
  const [to, setTo] = useState(seed.to);
  const [cc, setCc] = useState(seed.cc);
  const [bcc, setBcc] = useState(seed.bcc);
  const [showBook, setShowBook] = useState(false);
  const [showCopies, setShowCopies] = useState(seed.cc.length > 0 || seed.bcc.length > 0);
  const [subject, setSubject] = useState(seed.subject);
  const [format, setFormat] = useState<'html' | 'text'>(resume?.format ?? 'html');
  const [html, setHtml] = useState(initial.html);
  const [text, setText] = useState(initial.text);
  // La cita del original queda plegada fuera del editor hasta que el usuario la despliega.
  const [quoted, setQuoted] = useState(seed.quoted ?? null);
  // El editor no es controlado: cambiar de modo lo vuelve a montar con el contenido convertido.
  const [editorKey, setEditorKey] = useState(0);
  const [files, setFiles] = useState<File[]>(resume?.files ?? []);
  const [server, setServer] = useState<ServerAttachments | undefined>(seed.source);
  const [draftUid, setDraftUid] = useState(seed.draftUid);
  // De donde sale el borrador: del enlace (se sigue uno), de un guardado a mano o de uno automatico.
  const [draftOrigin, setDraftOrigin] = useState<'none' | 'seed' | 'manual' | 'auto'>(
    resume?.autosavedDraft ? 'auto' : seed.draftUid ? 'seed' : 'none',
  );
  // Dentro del lector, adjuntos, seguimiento y ficheros grandes se abren con el clip de la barra.
  const [showMore, setShowMore] = useState(!inline);
  // Al responder dentro del lector el asunto es el del hilo: se edita solo si se pide.
  const [showSubject, setShowSubject] = useState(
    !(inline && (mode === 'reply' || mode === 'replyAll')),
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
  const formRef = useRef<HTMLFormElement>(null);
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
      document.getElementById(ids.to)?.focus();
    }
    // En el lector la respuesta se abre debajo del mensaje: despues del foco (que solo desplaza
    // hasta el campo, y el editor lo toma un instante despues) se lleva entera a la vista, con
    // su cabecera bajo la barra del lector.
    if (!inline) return;
    const frame = window.requestAnimationFrame(() =>
      formRef.current?.scrollIntoView?.({ block: 'start' }),
    );
    return () => window.cancelAnimationFrame(frame);
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
  const fullHtml = withQuoted(html, quoted, 'html');
  const fullText = withQuoted(text, quoted, 'text');
  const body = format === 'html' ? fullHtml : fullText;
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
    text: format === 'html' ? '' : fullText,
    html: format === 'html' && htmlHasContent(fullHtml) ? fullHtml : undefined,
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
    if (mounted.current) onClose();
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
  // Lo que se retoma ya trae contenido: otra peticion no puede sustituirlo.
  const pristine = version === 0 && !resume && !waiting && !send.busy;
  useEffect(() => reportPristine?.(pristine), [pristine, reportPristine]);
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

  // En la ventana flotante o en el lector, cerrarla o navegar por el buzon guarda lo escrito
  // como borrador en lugar de perderlo (lo pendiente de guardar solo en los ultimos segundos).
  saveOnLeave.current = () => {
    if (!(windowed || inline) || closing.current || !dirty || !hasContent || blocked) return;
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
    // Con la cita plegada el editor solo tiene lo escrito: el enlace va al final, no delante.
    const quotedInEditor = mode !== null && mode !== 'draft' && quoted === null;
    if (source) setFiles((current) => current.filter((file) => file !== source));
    if (format === 'html') {
      setHtml((current) =>
        insertLargeFileLink(current, largeFileLinkHtml(shared), 'html', quotedInEditor),
      );
      setEditorKey((k) => k + 1);
    } else {
      setText((current) =>
        insertLargeFileLink(current, largeFileLinkText(shared), 'text', quotedInEditor),
      );
    }
    setVersion((v) => v + 1);
  };

  // Desplegar la cita la pasa al editor tal cual; el contenido que se envia no cambia.
  const unfoldQuoted = () => {
    if (!quoted) return;
    if (format === 'html') {
      setHtml(fullHtml);
      setEditorKey((k) => k + 1);
    } else {
      setText(fullText);
    }
    setQuoted(null);
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
    onClose();
  };

  const busy = send.busy || save.busy || waiting;
  const moreVisible =
    showMore ||
    files.length > 0 ||
    serverParts.length > 0 ||
    followUpDays > 0 ||
    Boolean(problems.attachments);

  // La ventana flotante recibe lo escrito tal cual; si ya hay otra redaccion abierta, se queda aqui.
  const popOut = () => {
    if (!composer) return;
    const opened = composer.open({
      kind: 'resume',
      mode,
      seed: {
        to,
        cc,
        bcc,
        subject,
        text: format === 'text' ? text : '',
        html: format === 'html' ? html : undefined,
        quoted: quoted ?? undefined,
        inReplyTo: seed.inReplyTo,
        draftUid,
        fromCandidates: from ? [from] : seed.fromCandidates,
        source: server,
      },
      files,
      format,
      autosavedDraft: draftOrigin === 'auto',
      recipientNames,
      assistantText: null,
    });
    if (!opened) return;
    closing.current = true;
    onClose();
  };
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
      ref={formRef}
      className="cf-wm-compose cf-form"
      onSubmit={submit}
      onKeyDown={(e) => {
        // Ctrl+Enter (Cmd+Enter en Mac) envia desde cualquier campo, como en Gmail.
        if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
          e.preventDefault();
          e.currentTarget.requestSubmit();
          return;
        }
        // Enter en un campo de una linea no envia: enviar es siempre un gesto explicito.
        const target = e.target as HTMLInputElement;
        if (e.key === 'Enter' && target.tagName === 'INPUT' && target.form === e.currentTarget) {
          e.preventDefault();
        }
      }}
      noValidate
      aria-labelledby={ids.title}
    >
      <ComposeHead
        title={windowed ? subject.trim() || t(TITLES[mode ?? 'new']) : t(TITLES[mode ?? 'new'])}
        titleId={ids.title}
        status={<AutosaveStatus state={autosave} dirty={dirty} />}
        onClose={onClose}
        onPopOut={inline && composer ? popOut : undefined}
        popOutDisabled={busy || autosaving}
      />
      {senders.length > 1 ? (
        <div className="cf-wm-compose__line">
          <FormField label={t('webmail.header.from')} htmlFor={ids.from}>
            <Select
              id={ids.from}
              options={senders.map((identity) => ({
                value: identity.email,
                label: identityLabel(identity),
              }))}
              value={from}
              onChange={(e) => edit(setChosenFrom)(e.target.value)}
              disabled={busy}
            />
          </FormField>
        </div>
      ) : null}
      {identities.error ? (
        <p className="cf-field__hint">{t('webmail.compose.identitiesUnavailable')}</p>
      ) : null}
      <div className="cf-wm-compose__line">
        <FormField
          label={t('webmail.header.to')}
          htmlFor={ids.to}
          error={recipientError ?? problems.recipients ?? null}
        >
          <RecipientInput
            id={ids.to}
            values={to}
            onChange={(values) => {
              edit(setTo)(values);
              setRecipientError(null);
            }}
            invalid={Boolean(recipientError ?? problems.recipients)}
            {...chips}
          />
        </FormField>
      </div>
      <div className="cf-wm-compose__extras">
        <Button
          size="sm"
          variant="ghost"
          onClick={() => setShowBook((open) => !open)}
          aria-expanded={showBook}
        >
          {t('webmail.compose.addressBook')}
        </Button>
        {showCopies ? null : (
          <Button size="sm" variant="ghost" onClick={() => setShowCopies(true)}>
            {t('webmail.compose.addCopies')}
          </Button>
        )}
        {showSubject || problems.subject ? null : (
          <Button size="sm" variant="ghost" onClick={() => setShowSubject(true)}>
            {t('webmail.compose.editSubject')}
          </Button>
        )}
      </div>
      {showBook ? (
        <AddressBookPicker
          chosen={[...to, ...cc, ...bcc]}
          onPick={(address) => {
            edit(setTo)([...to, address]);
            setRecipientError(null);
          }}
        />
      ) : null}
      {showCopies ? (
        <div className="cf-form__row">
          <div className="cf-wm-compose__line">
            <FormField label={t('webmail.header.cc')} htmlFor={ids.cc}>
              <RecipientInput id={ids.cc} values={cc} onChange={edit(setCc)} {...chips} />
            </FormField>
          </div>
          <div className="cf-wm-compose__line">
            <FormField label={t('webmail.header.bcc')} htmlFor={ids.bcc}>
              <RecipientInput id={ids.bcc} values={bcc} onChange={edit(setBcc)} {...chips} />
            </FormField>
          </div>
        </div>
      ) : null}
      {showSubject || problems.subject ? (
        <div className="cf-wm-compose__line">
          <FormField
            label={t('webmail.header.subject')}
            htmlFor={ids.subject}
            error={problems.subject ?? null}
          >
            <Input
              id={ids.subject}
              value={subject}
              onChange={(e) => edit(setSubject)(e.target.value)}
              disabled={busy}
            />
          </FormField>
        </div>
      ) : null}
      <div className="cf-field">
        <div className="cf-wm-compose__bodyhead">
          {format === 'html' ? (
            <span className="cf-field__label" id={ids.bodyLabel}>
              {t('webmail.compose.body')}
            </span>
          ) : (
            <label className="cf-field__label" htmlFor={ids.body} id={ids.bodyLabel}>
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
            id={ids.body}
            labelledBy={ids.bodyLabel}
            initialHtml={html}
            allowImages
            maxImageBytes={
              limits ? Math.min(limits.max_download_bytes, limits.max_message_bytes) : null
            }
            minHeight={inline ? '10rem' : '18rem'}
            onChange={edit(setHtml)}
            disabled={busy}
          />
        ) : (
          <Textarea
            ref={bodyRef}
            id={ids.body}
            rows={14}
            value={text}
            onChange={(e) => edit(setText)(e.target.value)}
            disabled={busy}
          />
        )}
        {quoted ? (
          <div>
            <Button
              size="sm"
              variant="ghost"
              className="cf-wm-compose__quoted"
              icon={<IconQuote size={14} />}
              aria-expanded={false}
              disabled={busy}
              onClick={unfoldQuoted}
            >
              {t('webmail.compose.showQuoted')}
            </Button>
          </div>
        ) : null}
      </div>
      <ComposeAssistant
        bodyText={() => (format === 'html' ? htmlToPlainText(html) : text)}
        replyTo={seed.inReplyTo}
        initialText={assistantText}
        disabled={busy}
        onInsert={insertAssistantText}
        onReplace={replaceWithAssistantText}
      />
      {moreVisible ? (
        <>
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
          <FormField label={t('webmail.followUp.label')} htmlFor={ids.followUp}>
            <Select
              id={ids.followUp}
              options={[
                { value: '0', label: t('webmail.followUp.none') },
                ...followUpChoices(limits?.max_reminder_days ?? null).map((days) => ({
                  value: String(days),
                  label:
                    days === 1
                      ? t('webmail.followUp.oneDay')
                      : t('webmail.followUp.days', { n: days }),
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
        </>
      ) : null}
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
        {inline ? (
          <Button
            variant="ghost"
            iconOnly
            title={t('webmail.compose.moreOptions')}
            icon={<IconPaperclip size={18} />}
            aria-expanded={moreVisible}
            disabled={busy}
            onClick={() => setShowMore((open) => !open)}
          >
            {t('webmail.compose.moreOptions')}
          </Button>
        ) : null}
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
          onClick={() => (dirty || draftOrigin === 'auto' ? setConfirmDiscard(true) : onClose())}
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
