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
  scheduled: 'scheduled',
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
    /** UIDs por operacion sobre varios mensajes. */
    max_batch_uids: number;
    /** Hasta cuantos dias en el futuro admite un envio programado. */
    max_scheduled_days: number;
    /** Mensajes de una conversacion que devuelve el servicio (sus UIDs y al abrirla). */
    max_thread_messages: number;
  };
  pagination: { default_page_size: number; max_page_size: number };
  folder_roles: string[];
  mutable_flags: string[];
  session: { idle_timeout_seconds: number; max_lifetime_seconds: number };
  /** Pestanas de la bandeja inteligente, en su orden (domain.Categories). */
  inbox_categories: string[];
}

/**
 * GET /webmail/meta/dav: topes de contactos y calendario tal como los sirve mail-dav. Se leen
 * solo con davLimits() de webmail/catalogs.ts.
 */
export interface DavMeta {
  limits: Partial<Record<string, number>>;
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
  /** Pestana de la bandeja inteligente; solo en los listados. */
  category?: string;
  /** Solo en el listado por conversaciones: la fila es el ultimo mensaje de la conversacion. */
  thread?: ThreadInfo;
}

/** Resumen de una conversacion (GET /folders/{folder}/messages?view=threads). */
export interface ThreadInfo {
  /** Mensajes de la conversacion en la carpeta. */
  size: number;
  unread: number;
  /** Del mas reciente al mas antiguo, como mucho limits.max_thread_messages. */
  uids: number[];
  participants: MailAddress[];
}

/** Un mensaje de una conversacion abierta (GET /threads): las respuestas propias estan en Enviados. */
export interface ConversationMessage extends MessageEnvelope {
  folder: string;
  message_id: string;
}

export type ShieldLevel = 'none' | 'info' | 'caution' | 'danger';

export interface ShieldReason {
  code: string;
  level: ShieldLevel;
  params: Record<string, string>;
}

export type UnsubscribeMethod = 'one_click' | 'mailto' | 'web';

/** GET /sender-insight: pestana, escudo antifraude y baja de un mensaje recibido. */
export interface SenderInsight {
  sender: MailAddress | null;
  category: string;
  shield: {
    level: ShieldLevel;
    external: boolean;
    /** El directorio de la empresa no respondio: no se comprobo todo. */
    partial: boolean;
    authentication: { spf: string | null; dkim: string | null; dmarc: string | null };
    reasons: ShieldReason[];
  };
  unsubscribe: {
    method: UnsubscribeMethod | null;
    /** Servidor (one_click) o direccion (mailto) que recibe la baja. */
    target: string;
    /** Solo con web: la pagina que el usuario abre por su cuenta. */
    url?: string;
  };
}

export interface UnsubscribeResult {
  method: Exclude<UnsubscribeMethod, 'web'>;
  target: string;
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

/** Filtros de la busqueda avanzada; las fechas en AAAA-MM-DD. */
export interface MessageFilters {
  from?: string;
  to?: string;
  subject?: string;
  since?: string;
  before?: string;
  unread?: boolean;
  flagged?: boolean;
  hasAttachments?: boolean;
}

export interface MessageQuery extends MessageFilters {
  page?: number;
  search?: string;
  /** Agrupa el listado por conversaciones. */
  view?: 'threads';
  /** Pestana de la bandeja inteligente (una de meta.inbox_categories). */
  category?: string;
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
  /** Cuerpo con formato; el servicio lo sanea y, sin texto, genera la parte de texto. */
  html?: string;
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

/** POST /send con send_at (202): el mensaje queda en Programados hasta su hora. */
export interface ScheduledRef {
  id: string;
  send_at: string;
}

export interface ScheduledSend {
  id: string;
  send_at: string;
  subject: string;
  recipients: string[];
  created_at: string;
  status: string;
}

export type BatchAction =
  | { action: 'flags'; add?: MutableFlag[]; remove?: MutableFlag[] }
  | { action: 'move'; to: string }
  | { action: 'delete' };

export interface BatchResult {
  affected: number;
  permanent: boolean;
}

export interface Signature {
  enabled: boolean;
  html: string;
  on_replies: boolean;
  /** Version en texto que genera el servicio. */
  text: string;
  updated_at: string | null;
  limits: { max_html_bytes: number; max_text_bytes: number };
}

export type SignatureInput = Pick<Signature, 'enabled' | 'html' | 'on_replies'>;

export const RULE_FIELDS = ['from', 'to', 'cc', 'recipient', 'subject'] as const;
export const RULE_OPERATORS = ['contains', 'not_contains', 'is'] as const;
export const RULE_ACTIONS = ['move', 'mark_read', 'flag', 'forward', 'discard'] as const;

export interface RuleCondition {
  field: (typeof RULE_FIELDS)[number];
  op: (typeof RULE_OPERATORS)[number];
  value: string;
}

export type RuleAction =
  | { type: 'move'; folder: string }
  | { type: 'mark_read' }
  | { type: 'flag' }
  | { type: 'forward'; address: string; keep_copy: boolean }
  | { type: 'discard' };

export interface MailRule {
  /** Vacio en una regla nueva: el directorio le asigna uno al guardar. */
  id: string;
  name: string;
  enabled: boolean;
  match: 'all' | 'any';
  conditions: RuleCondition[];
  actions: RuleAction[];
  stop: boolean;
}

export interface Forwarding {
  enabled: boolean;
  addresses: string[];
  keep_copy: boolean;
}

/** Topes que aplica mail-directory a las reglas y al reenvio. */
export interface FilterLimits {
  max_rules: number;
  max_conditions: number;
  max_actions: number;
  max_forward_addresses: number;
  /** Caracteres de cada valor de una condicion. */
  max_value_length: number;
  /** Caracteres del nombre de una regla. */
  max_name_length: number;
  /** Bytes del nombre de la carpeta de destino. */
  max_folder_bytes: number;
}

export interface MailFilters {
  rules: MailRule[];
  forwarding: Forwarding;
  updated_at: string | null;
  limits: FilterLimits;
}

/** El PUT solo admite los campos del contrato: sin limits ni updated_at (400). */
export type MailFiltersInput = Pick<MailFilters, 'rules' | 'forwarding'>;

export const CONTACT_VALUE_TYPES = ['home', 'work', 'mobile', 'other'] as const;
export type ContactValueType = (typeof CONTACT_VALUE_TYPES)[number];

export interface ContactValue {
  value: string;
  type: ContactValueType;
}

export interface ContactInput {
  name: string;
  given_name: string;
  family_name: string;
  emails: ContactValue[];
  phones: ContactValue[];
  organization: string;
  title: string;
  notes: string;
  /** AAAA-MM-DD o vacio. */
  birthday: string;
}

export interface Contact extends ContactInput {
  id: string;
  etag: string;
  updated_at: string;
}

export interface ContactQuery {
  q?: string;
  page?: number;
}

export interface ContactImportResult {
  imported: number;
  updated: number;
  skipped: { index: number; reason: string }[];
}

export const RECURRENCE_FREQUENCIES = ['daily', 'weekly', 'monthly', 'yearly'] as const;
export type RecurrenceFrequency = (typeof RECURRENCE_FREQUENCIES)[number];
/** Dias de la semana de iCalendar (RFC 5545), de lunes a domingo. */
export const WEEKDAYS = ['MO', 'TU', 'WE', 'TH', 'FR', 'SA', 'SU'] as const;
export type Weekday = (typeof WEEKDAYS)[number];

export interface Recurrence {
  freq: RecurrenceFrequency;
  interval: number;
  count: number | null;
  /** RFC 3339. */
  until: string | null;
  by_day: Weekday[] | null;
}

export interface CalendarEventInput {
  title: string;
  /** RFC 3339. Un evento de todo el dia va de las 00:00 UTC de su primer dia al dia siguiente al ultimo. */
  start: string;
  end: string;
  all_day: boolean;
  location: string;
  description: string;
  recurrence: Recurrence | null;
  reminder_minutes: number | null;
}

export interface CalendarEvent extends CalendarEventInput {
  id: string;
  etag: string;
}

export interface Occurrence {
  id: string;
  start: string;
  end: string;
  all_day: boolean;
  title: string;
  location: string;
  /** Pertenece a una serie: editarla cambia la serie entera. */
  recurring: boolean;
}

type Method = 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';

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
  if (input.html) form.append('html', input.html);
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

function flag(value: boolean | undefined): string | undefined {
  return value ? 'true' : undefined;
}

async function download(path: string, signal?: AbortSignal): Promise<DownloadedPart> {
  const res = await send('GET', path, { signal }, '*/*');
  return {
    blob: await res.blob(),
    filename: filenameFromDisposition(res.headers.get('Content-Disposition')),
    contentType: (res.headers.get('Content-Type') ?? '').split(';')[0]?.trim().toLowerCase() ?? '',
  };
}

function ifMatch(etag?: string): Record<string, string> | undefined {
  return etag ? { 'If-Match': etag } : undefined;
}

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
  davMeta: (signal?: AbortSignal) => request<DavMeta>('GET', wm.metaDav, { signal }),

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
      {
        params: {
          page: query.page,
          search: query.search,
          from: query.from,
          to: query.to,
          subject: query.subject,
          since: query.since,
          before: query.before,
          unread: flag(query.unread),
          flagged: flag(query.flagged),
          has_attachments: flag(query.hasAttachments),
          view: query.view,
          category: query.category,
        },
        signal,
      },
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
  downloadPart: (folder: string, uid: number, part: string, signal?: AbortSignal) =>
    download(wm.part(folder, uid, part), signal),

  /** El mensaje tal como esta en el buzon (message/rfc822), para guardarlo como .eml. */
  downloadRaw: (folder: string, uid: number, signal?: AbortSignal) =>
    download(wm.raw(folder, uid), signal),

  /** La conversacion del mensaje, de la mas antigua a la mas reciente. */
  conversation: async (
    folder: string,
    uid: number,
    signal?: AbortSignal,
  ): Promise<ConversationMessage[]> =>
    (await request<ConversationMessage[] | null>('GET', wm.threads, {
      params: { folder, uid },
      signal,
    })) ?? [],

  /** Ficha del mensaje: no lo marca como leido. */
  senderInsight: (folder: string, uid: number, signal?: AbortSignal) =>
    request<SenderInsight>('GET', wm.senderInsight, { params: { folder, uid }, signal }),

  /** La baja la decide el mensaje guardado: el cliente solo dice cual es. */
  unsubscribe: (folder: string, uid: number) =>
    request<UnsubscribeResult>('POST', wm.unsubscribe, { json: { folder, uid } }),

  /** Varios mensajes de la carpeta en una operacion; como mucho limits.max_batch_uids. */
  batch: (folder: string, uids: number[], action: BatchAction) =>
    request<BatchResult>('POST', wm.batch(folder), { json: { uids, ...action } }),

  /** Nombre completo de la carpeta, con el separador del servidor. */
  createFolder: (name: string) => request<WebmailFolder>('POST', wm.folders, { json: { name } }),
  renameFolder: (folder: string, name: string) =>
    request<WebmailFolder>('PATCH', wm.folder(folder), { json: { name } }),
  deleteFolder: (folder: string) => request<null>('DELETE', wm.folder(folder)),
  /** Solo Papelera y Spam. */
  emptyFolder: (folder: string) => request<{ removed: number }>('POST', wm.emptyFolder(folder)),

  /** Envia y, con replaceUid, retira ese borrador en la misma operacion. */
  send: (input: ComposeInput, options: SendOptions) =>
    request<SendResult>('POST', wm.send, {
      form: composeFormData(input, options.replaceUid),
      headers: { 'Idempotency-Key': options.idempotencyKey },
    }),

  /** Guarda en Borradores; replaceUid es el borrador anterior del mismo mensaje. */
  saveDraft: (input: ComposeInput, replaceUid?: number) =>
    request<{ uid: number }>('POST', wm.drafts, { form: composeFormData(input, replaceUid) }),

  /** Programa el envio para sendAt (RFC 3339); el mensaje espera en Programados. */
  schedule: async (input: ComposeInput, sendAt: string, options: SendOptions) => {
    const form = composeFormData(input, options.replaceUid);
    form.append('send_at', sendAt);
    const result = await request<{ scheduled: ScheduledRef }>('POST', wm.send, {
      form,
      headers: { 'Idempotency-Key': options.idempotencyKey },
    });
    return result.scheduled;
  },
  scheduled: async (signal?: AbortSignal): Promise<ScheduledSend[]> =>
    (await request<ScheduledSend[] | null>('GET', wm.scheduled, { signal })) ?? [],
  reschedule: (id: string, sendAt: string) =>
    request<ScheduledSend>('PATCH', wm.scheduledItem(id), { json: { send_at: sendAt } }),
  /** El mensaje vuelve a Borradores. */
  cancelScheduled: (id: string) => request<null>('DELETE', wm.scheduledItem(id)),

  signature: (signal?: AbortSignal) => request<Signature>('GET', wm.signature, { signal }),
  setSignature: (input: SignatureInput) => request<Signature>('PUT', wm.signature, { json: input }),

  filters: (signal?: AbortSignal) => request<MailFilters>('GET', wm.filters, { signal }),
  setFilters: (input: MailFiltersInput) => request<MailFilters>('PUT', wm.filters, { json: input }),

  /** 204: el cambio revoca todas las sesiones del buzon, tambien esta. */
  changePassword: (currentPassword: string, newPassword: string) =>
    request<null>('POST', wm.password, {
      json: { current_password: currentPassword, new_password: newPassword },
    }),

  contacts: async (query: ContactQuery, signal?: AbortSignal): Promise<Page<Contact>> => {
    const res = await send(
      'GET',
      wm.contacts,
      { params: { q: query.q, page: query.page }, signal },
      'application/json',
    );
    const json = await readEnvelope<Contact[] | null>(res);
    return toPage(
      { data: json?.data ?? [], meta: json?.meta },
      { page: query.page ?? 1, per_page: 0 },
    );
  },
  contact: (id: string, signal?: AbortSignal) =>
    request<Contact>('GET', wm.contact(id), { signal }),
  createContact: (input: ContactInput) => request<Contact>('POST', wm.contacts, { json: input }),
  /** Con etag, el servicio responde 412 si el contacto cambio desde que se leyo. */
  updateContact: (id: string, input: ContactInput, etag?: string) =>
    request<Contact>('PUT', wm.contact(id), { json: input, headers: ifMatch(etag) }),
  deleteContact: (id: string) => request<null>('DELETE', wm.contact(id)),
  exportContacts: (signal?: AbortSignal) => download(wm.contactsExport, signal),
  importContacts: (file: File) => {
    const form = new FormData();
    form.append('file', file, file.name);
    return request<ContactImportResult>('POST', wm.contactsImport, { form });
  },

  /** Ocurrencias entre start y end (RFC 3339); el servicio acota la ventana. */
  calendarOccurrences: async (
    start: string,
    end: string,
    signal?: AbortSignal,
  ): Promise<Occurrence[]> =>
    (await request<Occurrence[] | null>('GET', wm.calendarEvents, {
      params: { start, end },
      signal,
    })) ?? [],
  calendarEvent: (id: string, signal?: AbortSignal) =>
    request<CalendarEvent>('GET', wm.calendarEvent(id), { signal }),
  createCalendarEvent: (input: CalendarEventInput) =>
    request<CalendarEvent>('POST', wm.calendarEvents, { json: input }),
  updateCalendarEvent: (id: string, input: CalendarEventInput, etag?: string) =>
    request<CalendarEvent>('PUT', wm.calendarEvent(id), { json: input, headers: ifMatch(etag) }),
  /** Borra la serie entera. */
  deleteCalendarEvent: (id: string) => request<null>('DELETE', wm.calendarEvent(id)),
};

export function hasFlag(envelope: Pick<MessageEnvelope, 'flags'>, flag: string): boolean {
  return envelope.flags.includes(flag);
}
