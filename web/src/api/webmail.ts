import { apiBase, endpoints } from './endpoints';
import { ApiError, ERROR_CODES } from './errors';
import { toPage, type Envelope, type Page } from './types';

/*
 * Cliente del webmail (services/webmail). Es OTRA sesion: la del buzon, no la de la
 * plataforma.
 *
 * - La sesion es la cookie cf_wm (HttpOnly, SameSite=Strict, Path=/api/v1/webmail) que
 *   pone el servicio al iniciar sesion. Este codigo no la lee ni guarda ningun token: el
 *   navegador la envia sola, y solo a /api/v1/webmail.
 * - Nunca lleva el access token de la plataforma: no importa client.ts ni su sesion.
 * - credentials: 'include' solo aqui, y solo hacia las rutas de endpoints.webmail.
 * - CSRF: SameSite=Strict mas el Origin que el navegador pone en toda escritura y que el
 *   servicio exige (ORIGIN_NOT_ALLOWED). Un script no puede falsificar Origin.
 * - Un SESSION_EXPIRED en cualquier peticion avisa a quien escuche (el store del webmail
 *   vuelve al inicio de sesion del buzon).
 */

/** Papel de una carpeta especial (domain.FolderRole, RFC 6154). '' es una carpeta normal. */
export const FOLDER_ROLES = {
  inbox: 'inbox',
  sent: 'sent',
  drafts: 'drafts',
  trash: 'trash',
  junk: 'junk',
  archive: 'archive',
} as const;

/** Flags de sistema IMAP (RFC 3501) del API. El cliente solo cambia seen, flagged y answered. */
export const FLAGS = {
  seen: '\\Seen',
  answered: '\\Answered',
  flagged: '\\Flagged',
  draft: '\\Draft',
} as const;

export type MutableFlag = (typeof FLAGS)['seen' | 'flagged' | 'answered'];

// DTOs de services/webmail/internal/adapters/http/dto.go.

export interface WebmailQuota {
  used_bytes: number;
  limit_bytes: number;
}

export interface WebmailSession {
  username: string;
  display_name: string;
  expires_at: string;
  idle_timeout_seconds: number;
  /** null si Dovecot no respondio la cuota: es informativa. */
  quota: WebmailQuota | null;
}

export interface WebmailFolder {
  name: string;
  delimiter: string;
  role: string;
  selectable: boolean;
  total: number;
  unread: number;
}

export interface MailAddress {
  name: string;
  email: string;
}

export interface MessageEnvelope {
  uid: number;
  from: MailAddress[];
  to: MailAddress[];
  cc: MailAddress[];
  subject: string;
  date: string | null;
  flags: string[];
  size: number;
  has_attachments: boolean;
}

export interface MessagePart {
  part: string;
  filename: string;
  content_type: string;
  size: number;
  content_id: string;
  inline: boolean;
}

export interface MailMessage extends MessageEnvelope {
  folder: string;
  bcc: MailAddress[];
  reply_to: MailAddress[];
  message_id: string;
  in_reply_to: string[];
  references: string[];
  text: string;
  text_truncated: boolean;
  /** HTML ya saneado por el servicio. Solo se pinta en un iframe aislado. */
  html: string;
  html_truncated: boolean;
  remote_images: { present: boolean; blocked: boolean };
  attachments: MessagePart[];
}

export interface MessageQuery {
  page?: number;
  search?: string;
}

export interface ReadOptions {
  /** No marcar como leido al abrir. */
  peek?: boolean;
  /** Levanta el bloqueo de imagenes remotas solo para esta lectura. */
  allowRemoteImages?: boolean;
}

export interface FlagChange {
  add?: MutableFlag[];
  remove?: MutableFlag[];
}

export interface ReplyTarget {
  folder: string;
  uid: number;
}

/** Lo que se redacta. Las direcciones van ya validadas; el servicio las vuelve a validar. */
export interface ComposeInput {
  to: string[];
  cc: string[];
  bcc: string[];
  subject: string;
  text: string;
  inReplyTo?: ReplyTarget;
  attachments: File[];
}

export interface SendResult {
  message_id: string;
  /** false si el mensaje salio pero no se pudo guardar la copia en Enviados. */
  saved_to_sent: boolean;
}

export interface DownloadedPart {
  blob: Blob;
  /** Nombre ya saneado por el servicio (Content-Disposition). */
  filename: string | null;
  contentType: string;
}

type Method = 'GET' | 'POST' | 'DELETE';

interface RequestInit {
  params?: Record<string, string | number | boolean | undefined>;
  json?: unknown;
  form?: FormData;
  signal?: AbortSignal;
}

const expiredListeners = new Set<() => void>();

/** Avisa cuando el servicio responde SESSION_EXPIRED a cualquier peticion. */
export function onWebmailSessionExpired(listener: () => void): () => void {
  expiredListeners.add(listener);
  return () => expiredListeners.delete(listener);
}

function buildUrl(path: string, params?: RequestInit['params']): string {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(params ?? {})) {
    if (value === undefined || value === '') continue;
    query.set(key, String(value));
  }
  const qs = query.toString();
  return `${apiBase()}${path}${qs ? `?${qs}` : ''}`;
}

function isEnvelope(value: unknown): value is Envelope<unknown> {
  return typeof value === 'object' && value !== null;
}

async function toError(res: Response): Promise<ApiError> {
  let parsed: unknown = null;
  try {
    parsed = JSON.parse(await res.text());
  } catch {
    parsed = null;
  }
  const body = isEnvelope(parsed) && typeof parsed.error === 'object' ? parsed.error : null;
  let code = body?.code ?? '';
  // El limitador del gateway responde 429 sin el envelope de error del API.
  if (!code && res.status === 429) code = ERROR_CODES.RATE_LIMITED;
  if (!code && parsed === null) code = ERROR_CODES.INVALID_RESPONSE;
  const error = new ApiError(res.status, { code, message: body?.message ?? '' }, parsed);
  if (code === ERROR_CODES.SESSION_EXPIRED) expiredListeners.forEach((listener) => listener());
  return error;
}

async function send(method: Method, path: string, init: RequestInit, accept: string) {
  const headers: Record<string, string> = { Accept: accept };
  let body: BodyInit | undefined;
  if (init.json !== undefined) {
    headers['Content-Type'] = 'application/json';
    body = JSON.stringify(init.json);
  } else if (init.form) {
    // Sin Content-Type: el navegador pone multipart/form-data con su frontera.
    body = init.form;
  }
  let res: Response;
  try {
    res = await fetch(buildUrl(path, init.params), {
      method,
      headers,
      body,
      signal: init.signal,
      credentials: 'include',
      cache: 'no-store',
    });
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') throw err;
    throw new ApiError(0, { code: ERROR_CODES.NETWORK_ERROR, message: '' });
  }
  if (!res.ok) throw await toError(res);
  return res;
}

async function readEnvelope<T>(res: Response): Promise<Envelope<T> | null> {
  if (res.status === 204) return null;
  const raw = await res.text();
  if (!raw) return null;
  try {
    return JSON.parse(raw) as Envelope<T>;
  } catch {
    throw new ApiError(res.status, { code: ERROR_CODES.INVALID_RESPONSE, message: '' });
  }
}

async function request<T>(method: Method, path: string, init: RequestInit = {}): Promise<T> {
  const res = await send(method, path, init, 'application/json');
  const json = await readEnvelope<T>(res);
  return (json?.data ?? null) as T;
}

/**
 * Nombre de fichero de un Content-Disposition (RFC 6266): filename* (RFC 5987, UTF-8) si
 * lo trae, si no filename. El servicio ya lo sanea; aqui solo se lee.
 */
export function filenameFromDisposition(header: string | null): string | null {
  if (!header) return null;
  const extended = /filename\*\s*=\s*([^']*)'[^']*'([^;]+)/i.exec(header);
  if (extended?.[2]) {
    try {
      return decodeURIComponent(extended[2].trim());
    } catch {
      // Codificacion invalida: se intenta el filename simple.
    }
  }
  const plain = /filename\s*=\s*("(?:[^"\\]|\\.)*"|[^;]+)/i.exec(header);
  const value = plain?.[1]?.trim();
  if (!value) return null;
  return value.startsWith('"') ? value.slice(1, -1).replace(/\\(.)/g, '$1') : value;
}

/**
 * Formulario multipart de redaccion (compose_handler.go). Cada campo de direcciones viaja
 * como UNA lista separada por comas: el servicio admite listas RFC 5322 por valor.
 */
export function composeFormData(input: ComposeInput, replaceUid?: number): FormData {
  const form = new FormData();
  const list = (name: string, addresses: string[]) => {
    if (addresses.length) form.append(name, addresses.join(', '));
  };
  list('to', input.to);
  list('cc', input.cc);
  list('bcc', input.bcc);
  form.append('subject', input.subject);
  form.append('text', input.text);
  if (input.inReplyTo) {
    form.append('in_reply_to', String(input.inReplyTo.uid));
    form.append('in_reply_to_folder', input.inReplyTo.folder);
  }
  if (replaceUid) form.append('replace_uid', String(replaceUid));
  for (const file of input.attachments) form.append('attachments', file, file.name);
  return form;
}

const wm = endpoints.webmail;

export const webmailApi = {
  login: (username: string, password: string) =>
    request<WebmailSession>('POST', wm.session, { json: { username, password } }),
  session: (signal?: AbortSignal) => request<WebmailSession>('GET', wm.session, { signal }),
  logout: () => request<null>('DELETE', wm.session),

  folders: async (signal?: AbortSignal): Promise<WebmailFolder[]> =>
    (await request<WebmailFolder[] | null>('GET', wm.folders, { signal })) ?? [],

  /** Una pagina de la carpeta, del mas reciente al mas antiguo. El tamano lo fija el servicio. */
  messages: async (
    folder: string,
    query: MessageQuery,
    signal?: AbortSignal,
  ): Promise<Page<MessageEnvelope>> => {
    const res = await send(
      'GET',
      wm.messages(folder),
      { params: { page: query.page, search: query.search }, signal },
      'application/json',
    );
    const json = await readEnvelope<MessageEnvelope[] | null>(res);
    return toPage(
      { data: json?.data ?? [], meta: json?.meta },
      { page: query.page ?? 1, per_page: 0 },
    );
  },

  message: (folder: string, uid: number, options: ReadOptions = {}, signal?: AbortSignal) =>
    request<MailMessage>('GET', wm.message(folder, uid), {
      params: {
        peek: options.peek ? 'true' : undefined,
        remote_images: options.allowRemoteImages ? 'allow' : undefined,
      },
      signal,
    }),

  setFlags: (folder: string, uid: number, change: FlagChange) =>
    request<null>('POST', wm.flags(folder, uid), {
      json: { add: change.add ?? [], remove: change.remove ?? [] },
    }),

  move: (folder: string, uid: number, to: string) =>
    request<null>('POST', wm.move(folder, uid), { json: { to } }),

  /** A la papelera; desde la papelera, borrado definitivo (permanent). */
  remove: (folder: string, uid: number) =>
    request<{ permanent: boolean }>('DELETE', wm.message(folder, uid)),

  /** Una parte como bytes en memoria, para descargarla o incrustarla; nunca se navega a ella. */
  downloadPart: async (
    folder: string,
    uid: number,
    part: string,
    signal?: AbortSignal,
  ): Promise<DownloadedPart> => {
    const res = await send('GET', wm.part(folder, uid, part), { signal }, '*/*');
    return {
      blob: await res.blob(),
      filename: filenameFromDisposition(res.headers.get('Content-Disposition')),
      contentType:
        (res.headers.get('Content-Type') ?? '').split(';')[0]?.trim().toLowerCase() ?? '',
    };
  },

  send: (input: ComposeInput) =>
    request<SendResult>('POST', wm.send, { form: composeFormData(input) }),

  /** Guarda en Borradores; replaceUid es el borrador anterior del mismo mensaje. */
  saveDraft: (input: ComposeInput, replaceUid?: number) =>
    request<{ uid: number }>('POST', wm.drafts, { form: composeFormData(input, replaceUid) }),
};

export function hasFlag(envelope: Pick<MessageEnvelope, 'flags'>, flag: string): boolean {
  return envelope.flags.includes(flag);
}
