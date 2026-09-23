import { apiBase, endpoints } from './endpoints';
import { ApiError, ERROR_CODES } from './errors';
import { toPage, type Envelope, type Page } from './types';
import type { Vacation, VacationInput } from './vacation';

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

/** Flags de sistema IMAP (RFC 3501) del API. Los que el cliente puede cambiar llegan en la meta. */
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

/**
 * GET /webmail/meta (app.Meta): los topes y catalogos que el servicio aplica, sacados de su
 * dominio y su configuracion. La interfaz valida con ellos antes de enviar; no los copia.
 */
export interface WebmailMeta {
  limits: {
    max_recipients: number;
    max_message_bytes: number;
    max_attachments: number;
    /** Tope de descarga de una parte; tambien acota cada adjunto tomado del buzon. */
    max_download_bytes: number;
    /** Lo que se lee de un cuerpo de texto o HTML; mas alla, text_truncated/html_truncated. */
    max_body_part_bytes: number;
    max_subject_chars: number;
    max_search_bytes: number;
    max_folder_name_bytes: number;
  };
  pagination: { default_page_size: number; max_page_size: number };
  folder_roles: string[];
  mutable_flags: string[];
  session: { idle_timeout_seconds: number; max_lifetime_seconds: number };
}

/**
 * GET /webmail/identities: direcciones con las que el buzon puede enviar, las mismas que
 * Postfix le acepta (smtpd_sender_login_maps). El propio buzon va primero (primary).
 */
export interface SenderIdentity {
  email: string;
  name: string;
  primary: boolean;
}

/**
 * GET /webmail/address-book: un companero de la empresa del buzon. El servicio solo entrega
 * su direccion y su nombre visible (sin cuota, accesos ni credenciales).
 */
export interface AddressBookEntry {
  address: string;
  display_name: string;
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

/**
 * Partes de un mensaje del buzon que el servicio adjunta tomandolas del servidor (reenvio,
 * borrador que se sigue redactando): no se descargan ni se vuelven a subir.
 */
export interface PartSource {
  folder: string;
  uid: number;
  parts: string[];
}

/** Lo que se redacta. Las direcciones van ya validadas; el servicio las vuelve a validar. */
export interface ComposeInput {
  /** Uno de los remitentes del buzon; sin el, el propio buzon. */
  from?: string;
  to: string[];
  cc: string[];
  bcc: string[];
  subject: string;
  text: string;
  inReplyTo?: ReplyTarget;
  attachments: File[];
  source?: PartSource;
}

export interface SendOptions {
  /** Identifica el intento: repetirlo con la misma clave no vuelve a entregar el mensaje. */
  idempotencyKey: string;
  /** Borrador que el envio retira de Borradores en la misma operacion. */
  replaceUid?: number;
}

export interface SendResult {
  message_id: string;
  /** false si el mensaje salio pero no se pudo guardar la copia en Enviados. */
  saved_to_sent: boolean;
  /** true si se pidio retirar un borrador y ya no esta en Borradores. */
  draft_removed: boolean;
  /** La peticion repetia un envio ya hecho con la misma clave: no salio nada nuevo. */
  replayed: boolean;
}

export interface DownloadedPart {
  blob: Blob;
  /** Nombre ya saneado por el servicio (Content-Disposition). */
  filename: string | null;
  contentType: string;
}

type Method = 'GET' | 'POST' | 'PUT' | 'DELETE';

interface RequestInit {
  params?: Record<string, string | number | boolean | undefined>;
  json?: unknown;
  form?: FormData;
  headers?: Record<string, string>;
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

/*
 * Esperas antes de repetir una lectura que no llego a responder. Cubren el reinicio de un tramo
 * del camino (el borde, el gateway, el propio servicio o Dovecot al desplegar o renovar el
 * certificado), que corta las conexiones abiertas unos segundos: sin reintento, el usuario veia
 * "No se pudo conectar" y tenia que recargar. Solo GET: una escritura repetida podria aplicarse dos
 * veces, y el envio ya tiene su propia Idempotency-Key.
 */
const READ_RETRY_DELAYS_MS = [1000, 3000];

function isTransient(err: unknown): boolean {
  if (!(err instanceof ApiError)) return false;
  if (err.code === ERROR_CODES.NETWORK_ERROR) return true;
  if (err.status === 502 || err.status === 504) return true;
  // Otros 503 (avisos desactivados, antivirus caido) no se arreglan esperando unos segundos.
  return err.status === 503 && err.code === ERROR_CODES.SERVICE_UNAVAILABLE;
}

function wait(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DOMException('Aborted', 'AbortError'));
      return;
    }
    const timer = setTimeout(() => {
      signal?.removeEventListener('abort', onAbort);
      resolve();
    }, ms);
    const onAbort = () => {
      clearTimeout(timer);
      reject(new DOMException('Aborted', 'AbortError'));
    };
    signal?.addEventListener('abort', onAbort, { once: true });
  });
}

async function send(method: Method, path: string, init: RequestInit, accept: string) {
  if (method !== 'GET') return sendOnce(method, path, init, accept);
  for (const delay of READ_RETRY_DELAYS_MS) {
    try {
      return await sendOnce(method, path, init, accept);
    } catch (err) {
      if (!isTransient(err)) throw err;
      await wait(delay, init.signal);
    }
  }
  return sendOnce(method, path, init, accept);
}

async function sendOnce(method: Method, path: string, init: RequestInit, accept: string) {
  const headers: Record<string, string> = { ...init.headers, Accept: accept };
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
 * como UNA lista separada por comas: el servicio admite listas RFC 5322 por valor. Cada
 * parte del mensaje de origen va en su propio source_parts.
 */
export function composeFormData(input: ComposeInput, replaceUid?: number): FormData {
  const form = new FormData();
  const list = (name: string, addresses: string[]) => {
    if (addresses.length) form.append(name, addresses.join(', '));
  };
  if (input.from) form.append('from', input.from);
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
  if (input.source?.parts.length) {
    form.append('source_folder', input.source.folder);
    form.append('source_uid', String(input.source.uid));
    for (const part of input.source.parts) form.append('source_parts', part);
  }
  for (const file of input.attachments) form.append('attachments', file, file.name);
  return form;
}

const wm = endpoints.webmail;

/**
 * Flujo de avisos de la bandeja (Server-Sent Events). Va con la cookie del webmail y solo hacia
 * endpoints.webmail.events; no lleva ningun dato del correo: solo dice que hay algo que volver a leer.
 */
export function openWebmailEvents(): EventSource {
  return new EventSource(buildUrl(wm.events), { withCredentials: true });
}

export const webmailApi = {
  login: (username: string, password: string) =>
    request<WebmailSession>('POST', wm.session, { json: { username, password } }),
  session: (signal?: AbortSignal) => request<WebmailSession>('GET', wm.session, { signal }),
  logout: () => request<null>('DELETE', wm.session),

  /** Topes y catalogos del servicio. Se leen por sesion con webmail/catalogs.ts. */
  meta: (signal?: AbortSignal) => request<WebmailMeta>('GET', wm.meta, { signal }),

  /** Remitentes del buzon, el propio primero. Se leen por sesion con webmail/catalogs.ts. */
  identities: async (signal?: AbortSignal): Promise<SenderIdentity[]> =>
    (await request<SenderIdentity[] | null>('GET', wm.identities, { signal })) ?? [],

  /** Respuesta automatica del buzon de la sesion, con los topes que aplica el directorio. */
  vacation: (signal?: AbortSignal) => request<Vacation>('GET', wm.vacation, { signal }),
  setVacation: (input: VacationInput) => request<Vacation>('PUT', wm.vacation, { json: input }),

  /** Buzones activos de la empresa del buzon de la sesion; `query` filtra por direccion o nombre. */
  addressBook: async (query: string, signal?: AbortSignal): Promise<AddressBookEntry[]> =>
    (await request<AddressBookEntry[] | null>(
      'GET',
      `${wm.addressBook}?${new URLSearchParams({ q: query })}`,
      { signal },
    )) ?? [],

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

  /** Envia y, con replaceUid, retira ese borrador en la misma operacion. */
  send: (input: ComposeInput, options: SendOptions) =>
    request<SendResult>('POST', wm.send, {
      form: composeFormData(input, options.replaceUid),
      headers: { 'Idempotency-Key': options.idempotencyKey },
    }),

  /** Guarda en Borradores; replaceUid es el borrador anterior del mismo mensaje. */
  saveDraft: (input: ComposeInput, replaceUid?: number) =>
    request<{ uid: number }>('POST', wm.drafts, { form: composeFormData(input, replaceUid) }),
};

export function hasFlag(envelope: Pick<MessageEnvelope, 'flags'>, flag: string): boolean {
  return envelope.flags.includes(flag);
}
