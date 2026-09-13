import type { MailAddress, MailMessage, ReplyTarget } from '@/api/webmail';
import { formatDateTime } from '@/lib/format';
import { t } from '@/i18n';
import { addressList } from './format';

export const COMPOSE_MODES = ['reply', 'replyAll', 'forward', 'draft'] as const;
export type ComposeMode = (typeof COMPOSE_MODES)[number];

export function parseComposeMode(raw: string | null): ComposeMode | null {
  return COMPOSE_MODES.find((mode) => mode === raw) ?? null;
}

/** Punto de partida de una redaccion. */
export interface DraftSeed {
  to: string[];
  cc: string[];
  bcc: string[];
  subject: string;
  text: string;
  /** Mensaje al que se responde: el servicio encadena In-Reply-To y References. */
  inReplyTo?: ReplyTarget;
  /** Borrador que se esta editando: se reemplaza al guardar. */
  draftUid?: number;
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

/** Borrador inicial para responder, responder a todos, reenviar o seguir un borrador. */
export function buildDraft(mode: ComposeMode, message: MailMessage, ownAddress: string): DraftSeed {
  const body = messagePlainText(message);
  const sender = addressList(message.from) || t('common.dash');
  const date = formatDateTime(message.date);

  if (mode === 'draft') {
    return {
      to: recipients(message.to),
      cc: recipients(message.cc),
      bcc: recipients(message.bcc),
      subject: message.subject,
      text: body,
      draftUid: message.uid,
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
  return {
    to,
    cc,
    bcc: [],
    subject: prefixedSubject(t('webmail.compose.replyPrefix'), message.subject),
    text: `\n\n${t('webmail.compose.replyHeader', { date, sender })}\n${quote(body)}`,
    inReplyTo: { folder: message.folder, uid: message.uid },
  };
}
