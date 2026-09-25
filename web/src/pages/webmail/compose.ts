import { ERROR_CODES, errorCode, errorDetail } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import type {
  ComposeInput,
  MailAddress,
  MailMessage,
  MessagePart,
  ReplyTarget,
  SenderIdentity,
  Signature,
  WebmailMeta,
} from '@/api/webmail';
import { formatDateTime } from '@/lib/format';
import { formatBytes } from '@/lib/quota';
import { t } from '@/i18n';
import { addressList, utf8Length } from './format';
import { embedInlineImages, referencedInlineParts } from './inlineImages';
import { plainToHtml, signatureHtml, signatureText, textToHtml } from './richText';

export const COMPOSE_MODES = ['reply', 'replyAll', 'forward', 'draft'] as const;
export type ComposeMode = (typeof COMPOSE_MODES)[number];

export function parseComposeMode(raw: string | null): ComposeMode | null {
  return COMPOSE_MODES.find((mode) => mode === raw) ?? null;
}

/** Adjuntos de un mensaje del buzon que el servicio adjunta sin pasar por el navegador. */
export interface ServerAttachments {
  folder: string;
  uid: number;
  parts: MessagePart[];
}

/** Punto de partida de una redaccion. */
export interface DraftSeed {
  to: string[];
  cc: string[];
  bcc: string[];
  subject: string;
  text: string;
  /** HTML del borrador que se sigue redactando, si lo tenia; el editor lo limpia al cargarlo. */
  html?: string;
  /** Mensaje al que se responde: el servicio encadena In-Reply-To y References. */
  inReplyTo?: ReplyTarget;
  /** Borrador que se esta editando: se reemplaza al guardar y se retira al enviar. */
  draftUid?: number;
  /**
   * Direcciones que, si son remitentes del buzon, se proponen como remitente: la del
   * borrador que se sigue, o aquellas a las que llego el original que se responde.
   */
  fromCandidates?: string[];
  /** Adjuntos del original (reenvio) o del borrador que se sigue redactando. */
  source?: ServerAttachments;
}

export const EMPTY_DRAFT: DraftSeed = { to: [], cc: [], bcc: [], subject: '', text: '' };

const LOCAL_PART = /^[A-Za-z0-9!#$%&'*+/=?^_`{|}~.-]{1,64}$/;
const DOMAIN = /^[a-z0-9-]{1,63}(\.[a-z0-9-]{1,63})+$/;
const MAX_DOMAIN_LENGTH = 253;

/**
 * Espejo de domain.NewAddress del webmail: solo ASCII (Postfix descarta SMTPUTF8), la parte
 * local tal cual y el dominio en minusculas. El servicio vuelve a validar.
 */
export function normalizeRecipient(raw: string): string | null {
  const email = raw.trim().replace(/^<(.*)>$/, '$1');
  const at = email.lastIndexOf('@');
  if (at <= 0 || at === email.length - 1) return null;
  const local = email.slice(0, at);
  const host = email.slice(at + 1).toLowerCase();
  if (
    !LOCAL_PART.test(local) ||
    local.startsWith('.') ||
    local.endsWith('.') ||
    local.includes('..')
  ) {
    return null;
  }
  if (host.length > MAX_DOMAIN_LENGTH || !DOMAIN.test(host)) return null;
  return `${local}@${host}`;
}

const same = (a: string, b: string) => a.toLowerCase() === b.toLowerCase();

/** Direcciones validas, sin repetir y sin las excluidas. */
function recipients(list: readonly MailAddress[], exclude: readonly string[] = []): string[] {
  const out: string[] = [];
  for (const address of list) {
    const email = normalizeRecipient(address.email);
    if (!email) continue;
    if (exclude.some((x) => same(x, email)) || out.some((x) => same(x, email))) continue;
    out.push(email);
  }
  return out;
}

/** Antepone el prefijo (Re:, Fwd:) salvo que el asunto ya lo lleve. */
export function prefixedSubject(prefix: string, subject: string): string {
  const clean = subject.trim();
  return clean.toLowerCase().startsWith(prefix.toLowerCase()) ? clean : `${prefix} ${clean}`.trim();
}

/** Cita un texto linea a linea. */
export function quote(text: string): string {
  return text
    .split(/\r?\n/)
    .map((line) => (line ? `> ${line}` : '>'))
    .join('\n');
}

const BLOCK_ELEMENTS = new Set([
  'P',
  'DIV',
  'BR',
  'LI',
  'TR',
  'H1',
  'H2',
  'H3',
  'H4',
  'H5',
  'H6',
  'BLOCKQUOTE',
  'PRE',
  'TABLE',
  'HR',
  'UL',
  'OL',
  'SECTION',
  'ARTICLE',
  'HEADER',
  'FOOTER',
]);

/**
 * Texto de un HTML ya saneado, con saltos en los elementos de bloque. DOMParser crea un
 * documento inerte: no ejecuta nada ni carga recursos.
 */
export function htmlToPlainText(html: string): string {
  const doc = new DOMParser().parseFromString(html, 'text/html');
  let out = '';
  const walk = (node: Node) => {
    if (node.nodeType === Node.TEXT_NODE) {
      out += node.textContent ?? '';
      return;
    }
    if (node.nodeType !== Node.ELEMENT_NODE) return;
    const element = node as Element;
    if (element.tagName === 'SCRIPT' || element.tagName === 'STYLE') return;
    const block = BLOCK_ELEMENTS.has(element.tagName);
    if (block) out += '\n';
    element.childNodes.forEach(walk);
    if (block) out += '\n';
  };
  walk(doc.body);
  return out
    .replace(/[ \t\f\v\r]+/g, ' ')
    .replace(/ *\n */g, '\n')
    .replace(/\n{3,}/g, '\n\n')
    .trim();
}

/** Texto del mensaje: el text/plain si lo trae, si no el del HTML. */
export function messagePlainText(message: Pick<MailMessage, 'text' | 'html'>): string {
  if (message.text.trim()) return message.text;
  return message.html ? htmlToPlainText(message.html) : '';
}

/**
 * Adjuntos del original que se reenvian o que un borrador conserva: todos menos las
 * imagenes en linea que su HTML muestra, que viajan incrustadas en el cuerpo.
 */
export function forwardableParts(message: MailMessage): MessagePart[] {
  const inline = new Set(referencedInlineParts(message).values());
  return message.attachments.filter((part) => !inline.has(part));
}

function serverAttachments(message: MailMessage): ServerAttachments | undefined {
  const parts = forwardableParts(message);
  return parts.length ? { folder: message.folder, uid: message.uid, parts } : undefined;
}

/**
 * Borrador inicial para responder, responder a todos, reenviar o seguir un borrador. Con un
 * original en HTML la cita y el reenvio lo conservan con formato, y sus imagenes en linea van
 * incrustadas (`inlineImages`: URL de la parte -> data:, ya descargadas); el servicio las vuelve
 * a convertir en partes cid: al enviar. Un original solo en texto se cita linea a linea.
 */
export function buildDraft(
  mode: ComposeMode,
  message: MailMessage,
  ownAddress: string,
  inlineImages: ReadonlyMap<string, string> = new Map(),
): DraftSeed {
  const body = messagePlainText(message);
  const sender = addressList(message.from) || t('common.dash');
  const date = formatDateTime(message.date);
  const originalHtml = message.html.trim()
    ? embedInlineImages(message.html, inlineImages)
    : undefined;

  if (mode === 'draft') {
    return {
      to: recipients(message.to),
      cc: recipients(message.cc),
      bcc: recipients(message.bcc),
      subject: message.subject,
      text: body,
      html: originalHtml,
      draftUid: message.uid,
      fromCandidates: message.from.map((a) => a.email),
      source: serverAttachments(message),
    };
  }

  if (mode === 'forward') {
    const header = [
      t('webmail.compose.forwardHeader'),
      `${t('webmail.header.from')}: ${sender}`,
      `${t('webmail.header.date')}: ${date}`,
      `${t('webmail.header.subject')}: ${message.subject}`,
      `${t('webmail.header.to')}: ${addressList(message.to)}`,
    ];
    return {
      ...EMPTY_DRAFT,
      subject: prefixedSubject(t('webmail.compose.forwardPrefix'), message.subject),
      text: ['', '', ...header, '', body].join('\n'),
      html: originalHtml
        ? `${plainToHtml(['', '', ...header, ''].join('\n'))}<div>${originalHtml}</div>`
        : undefined,
      source: serverAttachments(message),
    };
  }

  const primary = message.reply_to.length ? message.reply_to : message.from;
  let to = recipients(primary);
  let cc: string[] = [];
  if (mode === 'replyAll') {
    // A todos menos a uno mismo; si solo quedaba uno mismo (un mensaje propio), a quien lo
    // envio.
    const all = recipients([...primary, ...message.to], [ownAddress]);
    to = all.length ? all : to;
    cc = recipients(message.cc, [ownAddress, ...to]);
  }
  const replyHeader = t('webmail.compose.replyHeader', { date, sender });
  return {
    to,
    cc,
    bcc: [],
    subject: prefixedSubject(t('webmail.compose.replyPrefix'), message.subject),
    text: `\n\n${replyHeader}\n${quote(body)}`,
    html: originalHtml
      ? `${plainToHtml(`\n\n${replyHeader}`)}<blockquote>${originalHtml}</blockquote>`
      : undefined,
    inReplyTo: { folder: message.folder, uid: message.uid },
    fromCandidates: [...message.to, ...message.cc].map((a) => a.email),
  };
}

/**
 * Cuerpo con el que se abre la redaccion, en texto y en HTML. La firma va en los mensajes
 * nuevos y, si el buzon lo pide, en respuestas y reenvios, encima de la cita; un borrador
 * que se sigue redactando ya la lleva.
 */
export function initialBody(
  seed: DraftSeed,
  mode: ComposeMode | null,
  signature: Pick<Signature, 'enabled' | 'on_replies' | 'html' | 'text'> | null,
): { text: string; html: string } {
  const withSignature =
    signature?.enabled === true &&
    (signature.html.trim() !== '' || signature.text.trim() !== '') &&
    (mode === null || (mode !== 'draft' && signature.on_replies));
  const seedHtml = seed.html ?? textToHtml(seed.text);
  if (!withSignature || !signature) {
    return { text: seed.text, html: seedHtml };
  }
  const signatureBody = signature.html.trim()
    ? signatureHtml(signature.html)
    : signatureHtml(textToHtml(signature.text));
  return {
    text: `${signatureText(signature.text)}${seed.text}`,
    html: `${signatureBody}${seedHtml}`,
  };
}

/**
 * Remitente con el que se abre la redaccion: el primer candidato que sea uno de los
 * remitentes del buzon; si no, el propio buzon. Sin lista, vacio: el servicio usa el buzon.
 */
export function pickSender(
  identities: readonly SenderIdentity[],
  candidates: readonly string[],
): string {
  for (const candidate of candidates) {
    const match = identities.find((identity) => same(identity.email, candidate));
    if (match) return match.email;
  }
  return identities.find((identity) => identity.primary)?.email ?? '';
}

/** "Nombre <direccion>" o la direccion sola. */
export function identityLabel(identity: SenderIdentity): string {
  const name = identity.name.trim();
  return name ? `${name} <${identity.email}>` : identity.email;
}

/** Lo que se mide antes de enviar. */
export interface ComposeCheck {
  to: readonly string[];
  cc: readonly string[];
  bcc: readonly string[];
  subject: string;
  text: string;
  files: readonly File[];
  serverParts: readonly MessagePart[];
}

export interface ComposeProblems {
  recipients?: string;
  subject?: string;
  attachments?: string;
}

/**
 * Topes del servicio (GET /webmail/meta) aplicados antes de enviar. Cuentan como el
 * servicio: destinatarios distintos sin mayusculas, asunto en caracteres, y el tamano con
 * el texto en UTF-8 y los adjuntos; el de los adjuntos del buzon es el que declara su
 * estructura. Sin meta no se comprueba nada aqui: el servicio lo hace siempre.
 */
export function composeProblems(
  input: ComposeCheck,
  limits: WebmailMeta['limits'] | null,
): ComposeProblems {
  if (!limits) return {};
  const problems: ComposeProblems = {};
  const unique = new Set([...input.to, ...input.cc, ...input.bcc].map((a) => a.toLowerCase()));
  if (unique.size > limits.max_recipients) {
    problems.recipients = t('webmail.compose.tooManyRecipients', {
      n: unique.size,
      max: limits.max_recipients,
    });
  }
  if (Array.from(input.subject).length > limits.max_subject_chars) {
    problems.subject = t('webmail.compose.subjectTooLong', { max: limits.max_subject_chars });
  }
  const count = input.files.length + input.serverParts.length;
  const bytes =
    utf8Length(input.subject) +
    utf8Length(input.text) +
    input.files.reduce((sum, file) => sum + file.size, 0) +
    input.serverParts.reduce((sum, part) => sum + part.size, 0);
  if (count > limits.max_attachments) {
    problems.attachments = t('webmail.compose.tooManyAttachments', {
      n: count,
      max: limits.max_attachments,
    });
  } else if (bytes > limits.max_message_bytes) {
    problems.attachments = t('webmail.compose.tooLarge', {
      size: formatBytes(bytes),
      max: formatBytes(limits.max_message_bytes),
    });
  }
  return problems;
}

/**
 * Huella de lo que se envia. Mientras no cambie, un reintento conserva la clave de
 * idempotencia y el servicio no vuelve a entregar el mensaje; otro contenido estrena clave.
 */
export function sendSignature(input: ComposeInput, replaceUid?: number): string {
  const { attachments, ...rest } = input;
  return JSON.stringify({
    ...rest,
    attachments: attachments.map((file) => [file.name, file.size, file.lastModified, file.type]),
    replaceUid: replaceUid ?? 0,
  });
}

/** Texto del error de un envio; un destinatario rechazado se nombra. */
export function composeErrorMessage(err: unknown): string {
  if (errorCode(err) === ERROR_CODES.RECIPIENT_REJECTED) {
    const address = errorDetail(err, 'address');
    if (address) return t('webmail.compose.recipientRejected', { address });
  }
  return errorMessage(err);
}
