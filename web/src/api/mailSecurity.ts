import { api } from './client';
import { endpoints } from './endpoints';
import { fetchList, fetchPage } from './paging';
import type { Page, PageQuery } from './types';

// DTOs de services/mail-security/internal/adapters/http/handler.go y domain/entities.go.
// Las puntuaciones son decimal.Decimal en Go y se serializan como cadena ("5.5"); se
// envian igual. El decodificador rechaza campos desconocidos: los cuerpos llevan
// exactamente los campos de cada handler.

export type ListKind = 'allow' | 'deny';
export const LIST_KINDS: readonly ListKind[] = ['allow', 'deny'];

/** Unidades que entiende ratelimit.lua de Rspamd en "N / 1h". */
export const RATE_LIMIT_UNITS = ['s', 'm', 'h', 'd'] as const;
export type RateLimitUnit = (typeof RATE_LIMIT_UNITS)[number];

/** Tope de per_page de la cuarentena (maxQuarantinePerPage). */
export const QUARANTINE_MAX_PAGE_SIZE = 200;

/** Tope de mensajes de una consulta de la cola de Postfix (domain.MaxQueueListLimit). */
export const QUEUE_MAX_LIMIT = 500;

/** Acciones sobre un mensaje de la cola que no lo borran (POST /queue/{id}/{action}). */
export type QueueAction = 'retry' | 'hold' | 'unhold';

export interface QueueRecipient {
  address: string;
  delay_reason?: string;
}

/** Un mensaje de la cola de Postfix de la celda, sin su contenido (domain.QueueMessage). */
export interface QueueMessage {
  queue_id: string;
  /** Cola de Postfix: incoming, active, deferred, hold o corrupt. */
  queue_name: string;
  /** Segundos desde epoch. */
  arrival_time: number;
  message_size: number;
  /** Vacio en los rebotes (remitente nulo). */
  sender: string;
  recipients: QueueRecipient[];
  recipients_total: number;
  recipients_capped?: boolean;
}

export interface QueueListing {
  /** Todos los mensajes de la cola, aunque items traiga menos. */
  total: number;
  truncated: boolean;
  items: QueueMessage[];
}

/** Tope de filas del historial de Rspamd (domain.MaxRspamdHistoryRows). */
export const RSPAMD_HISTORY_MAX_ROWS = 200;

/** Contadores del controller de Rspamd de la celda (domain.RspamdStats). Solo lectura. */
export interface RspamdStats {
  version: string;
  uptime_seconds: number;
  scanned: number;
  learned: number;
  spam_count: number;
  ham_count: number;
  /** Mensajes por veredicto: reject, add header, greylist, no action... */
  actions: Record<string, number>;
  connections: number;
  control_connections: number;
  total_learns: number;
  statfiles: RspamdStatfile[];
  /** Hashes por almacen fuzzy. */
  fuzzy_hashes: Record<string, number>;
  scan_time: {
    samples: number;
    /** Decimales serializados como cadena. */
    average_ms: string;
    max_ms: string;
  };
}

export interface RspamdStatfile {
  symbol: string;
  type: string;
  revision: number;
  used: number;
  total: number;
  size: number;
  languages: number;
  users: number;
}

export interface RspamdSymbol {
  name: string;
  score: string;
}

/** Sobre y veredicto de un mensaje analizado, nunca su contenido (domain.RspamdHistoryRow). */
export interface RspamdHistoryRow {
  id: string;
  /** RFC 3339. */
  time: string;
  ip: string;
  user?: string;
  sender: string;
  recipients: string[];
  subject: string;
  score: string;
  required_score: string;
  action: string;
  symbols: RspamdSymbol[];
  size: number;
  scan_time_ms: string;
  skipped: boolean;
}

export interface RspamdHistory {
  /** Filas que el controller devolvio, aunque rows traiga menos. */
  total: number;
  truncated: boolean;
  rows: RspamdHistoryRow[];
}

export interface SpamScore {
  id: string;
  tenant_id: string;
  object: string;
  high_score: string;
  low_score: string;
  created_at: string;
  updated_at: string;
}

export interface SpamScoreInput {
  high_score: string;
  low_score: string;
}

export interface AddressListEntry {
  id: string;
  tenant_id: string;
  object: string;
  kind: ListKind;
  pattern: string;
  created_at: string;
  updated_at: string;
}

export interface AddressListQuery {
  object?: string;
  kind?: ListKind;
}

export interface CreateAddressListRequest {
  object: string;
  kind: ListKind;
  pattern: string;
}

export interface DomainFooter {
  id: string;
  tenant_id: string;
  domain: string;
  html: string;
  plain: string;
  mailbox_exclude: string[] | null;
  alias_domain_exclude: string[] | null;
  skip_replies: boolean;
  created_at: string;
  updated_at: string;
}

export interface FooterInput {
  html: string;
  plain: string;
  mailbox_exclude: string[];
  alias_domain_exclude: string[];
  skip_replies: boolean;
}

export interface ForwardingHost {
  id: string;
  tenant_id: string;
  host: string;
  source: string;
  filter_spam: boolean;
  created_at: string;
  updated_at: string;
}

export interface CreateForwardingHostRequest {
  host: string;
  source: string;
  filter_spam: boolean;
}

export interface RateLimit {
  id: string;
  tenant_id: string;
  object: string;
  value: string;
  created_at: string;
  updated_at: string;
}

export interface MailboxTags {
  id: string;
  tenant_id: string;
  username: string;
  subject_tag: boolean;
  subfolder_tag: boolean;
  created_at: string;
  updated_at: string;
}

export interface MailboxTagsInput {
  subject_tag: boolean;
  subfolder_tag: boolean;
}

export interface QuarantineItem {
  id: string;
  tenant_id: string;
  qid: string;
  subject: string;
  score: string;
  ip: string;
  action: string;
  symbols: string[] | null;
  fuzzy_hashes: string[] | null;
  sender: string;
  rcpt: string;
  domain: string;
  notified: boolean;
  user_name: string;
  qhash: string;
  size: number;
  created_at: string;
}

export interface QuarantineQuery extends PageQuery {
  rcpt?: string;
  score_min?: string;
}

export interface QuarantineNotify {
  enabled: boolean;
  max_score: string;
  sender: string;
  subject: string;
  html_template: string;
}

/**
 * Datos que recibe notify.html_template (domain.QuarantineNoticeData, html/template de Go,
 * que escapa todo segun el contexto). mail-security no publica un catalogo de variables:
 * esto es el contrato que documenta su dominio y hay que mantenerlo a la par.
 */
export const QUARANTINE_NOTICE_TEMPLATE = {
  fields: ['.Mailbox', '.Count', '.LinksExpireAt'],
  list: '.Messages',
  itemFields: ['.Subject', '.Sender', '.Date', '.Score', '.ReleaseURL', '.DiscardURL'],
} as const;

export interface QuarantineSettings {
  tenant_id: string;
  max_size_bytes: number;
  max_age_days: number;
  retention_size: number;
  exclude_domains: string[] | null;
  notify: QuarantineNotify;
  updated_at: string;
}

export interface QuarantineSettingsInput {
  max_size_bytes: number;
  max_age_days: number;
  retention_size: number;
  exclude_domains: string[];
  notify: QuarantineNotify;
}

interface StatusResponse {
  status: string;
}

export const mailSecurityApi = {
  listSpamScores: () => fetchList<SpamScore>(endpoints.mailSecurity.spamScores),
  putSpamScore: (object: string, input: SpamScoreInput) =>
    api.put<SpamScore>(endpoints.mailSecurity.spamScore(object), { body: input }),
  deleteSpamScore: (object: string) => api.delete<null>(endpoints.mailSecurity.spamScore(object)),

  listAddressLists: (query: AddressListQuery) =>
    fetchList<AddressListEntry>(endpoints.mailSecurity.addressLists, { ...query }),
  createAddressList: (input: CreateAddressListRequest) =>
    api.post<AddressListEntry>(endpoints.mailSecurity.addressLists, { body: input }),
  deleteAddressList: (id: string) => api.delete<null>(endpoints.mailSecurity.addressList(id)),

  listFooters: () => fetchList<DomainFooter>(endpoints.mailSecurity.footers),
  putFooter: (domain: string, input: FooterInput) =>
    api.put<DomainFooter>(endpoints.mailSecurity.footer(domain), { body: input }),
  deleteFooter: (domain: string) => api.delete<null>(endpoints.mailSecurity.footer(domain)),

  listForwardingHosts: () => fetchList<ForwardingHost>(endpoints.mailSecurity.forwardingHosts),
  createForwardingHost: (input: CreateForwardingHostRequest) =>
    api.post<ForwardingHost>(endpoints.mailSecurity.forwardingHosts, { body: input }),
  deleteForwardingHost: (id: string) => api.delete<null>(endpoints.mailSecurity.forwardingHost(id)),

  listRateLimits: () => fetchList<RateLimit>(endpoints.mailSecurity.rateLimits),
  putRateLimit: (object: string, value: string) =>
    api.put<RateLimit>(endpoints.mailSecurity.rateLimit(object), { body: { value } }),
  deleteRateLimit: (object: string) => api.delete<null>(endpoints.mailSecurity.rateLimit(object)),

  listMailboxTags: () => fetchList<MailboxTags>(endpoints.mailSecurity.mailboxTags),
  putMailboxTags: (username: string, input: MailboxTagsInput) =>
    api.put<MailboxTags>(endpoints.mailSecurity.mailboxTag(username), { body: input }),

  listQuarantine: (query: QuarantineQuery): Promise<Page<QuarantineItem>> =>
    fetchPage<QuarantineItem>(endpoints.mailSecurity.quarantine, { ...query }),
  getQuarantine: (id: string) => api.get<QuarantineItem>(endpoints.mailSecurity.quarantineItem(id)),
  /** El mensaje original (message/rfc822). Es correo sospechoso: se muestra solo como texto. */
  quarantineMessage: (id: string) =>
    api.getText(endpoints.mailSecurity.quarantineMessage(id), {
      accept: 'message/rfc822, application/json',
    }),
  releaseQuarantine: (id: string) =>
    api.post<StatusResponse>(endpoints.mailSecurity.quarantineRelease(id)),
  /** Libera el mensaje y lo usa para entrenar el clasificador como legitimo (permisos release y learn). */
  releaseQuarantineAsHam: (id: string) =>
    api.post<StatusResponse>(endpoints.mailSecurity.quarantineReleaseHam(id)),
  learnSpam: (id: string) =>
    api.post<StatusResponse>(endpoints.mailSecurity.quarantineLearnSpam(id)),
  deleteQuarantine: (id: string) => api.delete<null>(endpoints.mailSecurity.quarantineItem(id)),

  /** Cola de Postfix de la celda: solo el superadmin (permiso mail_security/queue, de plataforma). */
  listQueue: async (limit: number): Promise<QueueListing> =>
    (await api.get<QueueListing>(endpoints.mailSecurity.queue, { params: { limit } })).data,
  queueAction: (id: string, action: QueueAction) =>
    api.post<null>(endpoints.mailSecurity.queueAction(id, action)),
  deleteQueueMessage: (id: string) => api.delete<null>(endpoints.mailSecurity.queueMessage(id)),
  flushQueue: () => api.post<StatusResponse>(endpoints.mailSecurity.queueFlush),
  /** Lectura del controller de Rspamd de la celda: solo el superadmin (permiso mail_security/rspamd/read). */
  rspamdStats: async (signal?: AbortSignal) =>
    (await api.get<RspamdStats>(endpoints.mailSecurity.rspamdStats, { signal })).data,
  rspamdHistory: async (limit: number, signal?: AbortSignal) =>
    (await api.get<RspamdHistory>(endpoints.mailSecurity.rspamdHistory, { params: { limit }, signal }))
      .data,

  getQuarantineSettings: () =>
    api.get<QuarantineSettings>(endpoints.mailSecurity.quarantineSettings),
  putQuarantineSettings: (input: QuarantineSettingsInput) =>
    api.put<QuarantineSettings>(endpoints.mailSecurity.quarantineSettings, { body: input }),
};
