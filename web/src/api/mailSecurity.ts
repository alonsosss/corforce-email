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
  learnSpam: (id: string) =>
    api.post<StatusResponse>(endpoints.mailSecurity.quarantineLearnSpam(id)),
  deleteQuarantine: (id: string) => api.delete<null>(endpoints.mailSecurity.quarantineItem(id)),

  getQuarantineSettings: () =>
    api.get<QuarantineSettings>(endpoints.mailSecurity.quarantineSettings),
  putQuarantineSettings: (input: QuarantineSettingsInput) =>
    api.put<QuarantineSettings>(endpoints.mailSecurity.quarantineSettings, { body: input }),
};
