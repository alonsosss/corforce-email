import { api } from './client';
import { endpoints } from './endpoints';
import { fetchList, fetchPage } from './paging';
import type { ApiResponse, Page, PageQuery } from './types';

// DTOs de services/mail-directory: peticiones en internal/adapters/http/dto.go y
// respuestas en internal/domain/entities.go. Las cuotas viajan en bytes y 0 significa sin
// limite; los maximos de buzones y aliases, igual.

/** Estado que leen Postfix y Dovecot: 0 no recibe ni envia, 1 activo, 2 solo recibe. */
export type ActiveState = 0 | 1 | 2;
export const ACTIVE_STATES: readonly ActiveState[] = [1, 2, 0];

/** Espejo de domain.TLSPolicies(): lo que admite smtp_tls_policy_maps de Postfix. */
export const TLS_POLICIES = [
  'none',
  'may',
  'encrypt',
  'dane',
  'dane-only',
  'fingerprint',
  'verify',
  'secure',
] as const;
export type TlsPolicyName = (typeof TLS_POLICIES)[number];

export const BCC_TYPES = ['sender', 'rcpt'] as const;
export type BccType = (typeof BCC_TYPES)[number];

// Espejo de domain.MinPasswordLength, domain.MaxPasswordLength y
// domain.MaxSieveScriptBytes, y del tope de pagina de app.NormalizePage.
export const MAILBOX_PASSWORD_MIN_LENGTH = 12;
export const MAILBOX_PASSWORD_MAX_LENGTH = 256;
export const SIEVE_SCRIPT_MAX_BYTES = 64 * 1024;
export const DIRECTORY_MAX_PAGE_SIZE = 200;

// ── Dominios del directorio ─────────────────────────────────────────────────

export interface DirectoryDomain {
  id: string;
  tenant_id: string;
  domain: string;
  description: string;
  active: boolean;
  backupmx: boolean;
  relay_all_recipients: boolean;
  relay_unknown_only: boolean;
  relayhost_id: string | null;
  max_aliases: number;
  max_mailboxes: number;
  default_quota_bytes: number;
  max_quota_bytes: number;
  quota_bytes: number;
  created_at: string;
  updated_at: string;
}

export interface UpdateDirectoryDomainRequest {
  description?: string;
  backupmx?: boolean;
  relay_all_recipients?: boolean;
  relay_unknown_only?: boolean;
  /** null desvincula el relayhost; ausente lo deja como esta. */
  relayhost_id?: string | null;
  max_aliases?: number;
  max_mailboxes?: number;
  default_quota_bytes?: number;
  max_quota_bytes?: number;
  quota_bytes?: number;
}

export interface AliasDomain {
  id: string;
  tenant_id: string;
  alias_domain: string;
  target_domain: string;
  active: boolean;
  created_at: string;
  updated_at: string;
}

export interface CreateAliasDomainRequest {
  alias_domain: string;
  target_domain: string;
  active?: boolean;
}

export interface UpdateAliasDomainRequest {
  target_domain?: string;
  active?: boolean;
}

// ── Buzones ─────────────────────────────────────────────────────────────────

export interface MailboxAccess {
  imap_access: boolean;
  pop3_access: boolean;
  smtp_access: boolean;
  sieve_access: boolean;
}

export interface Mailbox extends MailboxAccess {
  id: string;
  tenant_id: string;
  username: string;
  local_part: string;
  domain: string;
  display_name: string;
  quota_bytes: number;
  active: ActiveState;
  kind: string;
  tls_enforce_in: boolean;
  tls_enforce_out: boolean;
  relayhost_id: string | null;
  force_pw_update: boolean;
  created_at: string;
  updated_at: string;
}

export interface CreateMailboxRequest extends Partial<MailboxAccess> {
  local_part: string;
  domain: string;
  password: string;
  display_name: string;
  /** Ausente: la cuota por defecto del dominio. */
  quota_bytes?: number;
  active?: ActiveState;
  tls_enforce_in: boolean;
  tls_enforce_out: boolean;
}

export interface UpdateMailboxRequest extends Partial<MailboxAccess> {
  display_name?: string;
  quota_bytes?: number;
  active?: ActiveState;
  tls_enforce_in?: boolean;
  tls_enforce_out?: boolean;
  force_pw_update?: boolean;
}

export interface QuotaUsage {
  quota_bytes: number;
  used_bytes: number;
  messages: number;
}

export interface SaslLogin {
  id: string;
  username: string;
  service: string;
  app_password_id: string | null;
  remote_ip: string;
  logged_at: string;
}

export interface AppPasswordAccess extends MailboxAccess {
  dav_access: boolean;
}

export interface AppPassword extends AppPasswordAccess {
  id: string;
  tenant_id: string;
  mailbox_id: string;
  name: string;
  active: boolean;
  last_used_at: string | null;
  created_at: string;
  updated_at: string;
}

export interface CreateAppPasswordRequest extends Partial<AppPasswordAccess> {
  name: string;
}

export interface UpdateAppPasswordRequest extends Partial<AppPasswordAccess> {
  name?: string;
  active?: boolean;
}

/** Respuesta del alta: la contrasena en claro viaja una sola vez y no se puede recuperar. */
export interface CreatedAppPassword {
  app_password: AppPassword;
  password: string;
}

export type SieveFilterType = 'prefilter' | 'postfilter';

export interface SieveFilter {
  id: string;
  tenant_id: string;
  username: string;
  filter_type: SieveFilterType;
  script_desc: string;
  script_data: string;
  active: boolean;
  created_at: string;
  updated_at: string;
}

export interface MailboxSieve {
  prefilter: SieveFilter | null;
  postfilter: SieveFilter | null;
}

export interface SieveScriptInput {
  script_desc: string;
  script_data: string;
  active: boolean;
}

/** PUT reemplaza los dos filtros a la vez: null borra ese filtro. */
export interface PutSieveRequest {
  prefilter: SieveScriptInput | null;
  postfilter: SieveScriptInput | null;
}

// ── Enrutado ────────────────────────────────────────────────────────────────

export interface MailAlias {
  id: string;
  tenant_id: string;
  address: string;
  /** Destinos separados por comas. */
  goto: string;
  domain: string;
  sender_allowed: boolean;
  internal: boolean;
  active: ActiveState;
  private_comment: string;
  public_comment: string;
  created_at: string;
  updated_at: string;
}

export interface CreateAliasRequest {
  address: string;
  goto: string;
  sender_allowed?: boolean;
  internal: boolean;
  active?: ActiveState;
  private_comment: string;
  public_comment: string;
}

export interface UpdateAliasRequest {
  goto?: string;
  sender_allowed?: boolean;
  internal?: boolean;
  active?: ActiveState;
  private_comment?: string;
  public_comment?: string;
}

export interface SpamAlias {
  id: string;
  tenant_id: string;
  address: string;
  goto: string;
  description: string;
  valid_until: string | null;
  permanent: boolean;
  created_at: string;
  updated_at: string;
}

export interface CreateSpamAliasRequest {
  address: string;
  goto: string;
  description: string;
  valid_until?: string;
  permanent: boolean;
}

export interface UpdateSpamAliasRequest {
  description?: string;
  valid_until?: string;
  permanent?: boolean;
}

export interface SenderAcl {
  id: string;
  tenant_id: string;
  logged_in_as: string;
  send_as: string;
  external: boolean;
  created_at: string;
}

export interface CreateSenderAclRequest {
  logged_in_as: string;
  send_as: string;
  external: boolean;
}

export interface UpdateSenderAclRequest {
  send_as?: string;
  external?: boolean;
}

export interface Relayhost {
  id: string;
  tenant_id: string;
  hostname: string;
  username: string;
  /** La contrasena nunca se devuelve: solo si hay una guardada. */
  has_password: boolean;
  active: boolean;
  created_at: string;
  updated_at: string;
}

export interface CreateRelayhostRequest {
  hostname: string;
  username: string;
  password: string;
  active?: boolean;
}

export interface UpdateRelayhostRequest {
  hostname?: string;
  username?: string;
  /** "" borra la contrasena guardada; ausente la conserva. */
  password?: string;
  active?: boolean;
}

export interface Transport {
  id: string;
  /** null: ruta de plataforma, visible para todas las empresas y solo editable por el operador. */
  tenant_id: string | null;
  destination: string;
  nexthop: string;
  username: string;
  has_password: boolean;
  is_mx_based: boolean;
  active: boolean;
  created_at: string;
  updated_at: string;
}

export interface CreateTransportRequest {
  destination: string;
  nexthop: string;
  username: string;
  password: string;
  is_mx_based: boolean;
  active?: boolean;
  platform: boolean;
}

export interface UpdateTransportRequest {
  destination?: string;
  nexthop?: string;
  username?: string;
  password?: string;
  is_mx_based?: boolean;
  active?: boolean;
}

export function isPlatformTransport(transport: Transport): boolean {
  return transport.tenant_id === null;
}

export interface TlsPolicy {
  id: string;
  tenant_id: string;
  dest: string;
  policy: TlsPolicyName;
  parameters: string;
  active: boolean;
  created_at: string;
  updated_at: string;
}

export interface CreateTlsPolicyRequest {
  dest: string;
  policy: TlsPolicyName;
  parameters: string;
  active?: boolean;
}

export interface UpdateTlsPolicyRequest {
  policy?: TlsPolicyName;
  parameters?: string;
  active?: boolean;
}

export interface RecipientMap {
  id: string;
  tenant_id: string;
  old_dest: string;
  new_dest: string;
  active: boolean;
  created_at: string;
  updated_at: string;
}

export interface CreateRecipientMapRequest {
  old_dest: string;
  new_dest: string;
  active?: boolean;
}

export interface UpdateRecipientMapRequest {
  new_dest?: string;
  active?: boolean;
}

export interface BccMap {
  id: string;
  tenant_id: string;
  local_dest: string;
  bcc_dest: string;
  domain: string;
  type: BccType;
  active: boolean;
  created_at: string;
  updated_at: string;
}

export interface CreateBccMapRequest {
  local_dest: string;
  bcc_dest: string;
  type: BccType;
  active?: boolean;
}

export interface UpdateBccMapRequest {
  bcc_dest?: string;
  type?: BccType;
  active?: boolean;
}

// ── Cliente ─────────────────────────────────────────────────────────────────

/** Recurso con listado paginado, alta, modificacion parcial y baja. */
export interface ResourceApi<T, C, U> {
  list: (query: PageQuery) => Promise<Page<T>>;
  create: (input: C) => Promise<ApiResponse<T>>;
  update: (id: string, input: U) => Promise<ApiResponse<T>>;
  remove: (id: string) => Promise<ApiResponse<null>>;
}

interface CollectionEndpoints {
  collection: string;
  byId: (id: string) => string;
}

function resourceApi<T, C, U>(ep: CollectionEndpoints): ResourceApi<T, C, U> {
  return {
    list: (query) => fetchPage<T>(ep.collection, { ...query }),
    create: (input) => api.post<T>(ep.collection, { body: input }),
    update: (id, input) => api.patch<T>(ep.byId(id), { body: input }),
    remove: (id) => api.delete<null>(ep.byId(id)),
  };
}

export const mailDirectoryApi = {
  listDomains: (query: PageQuery) =>
    fetchPage<DirectoryDomain>(endpoints.mailDomains.collection, { ...query }),
  updateDomain: (id: string, input: UpdateDirectoryDomainRequest) =>
    api.patch<DirectoryDomain>(endpoints.mailDomains.byId(id), { body: input }),
  aliasDomains: resourceApi<AliasDomain, CreateAliasDomainRequest, UpdateAliasDomainRequest>(
    endpoints.mailDomains.aliasDomains,
  ),

  listMailboxes: (query: PageQuery) =>
    fetchPage<Mailbox>(endpoints.mailboxes.collection, { ...query }),
  getMailbox: (id: string) => api.get<Mailbox>(endpoints.mailboxes.byId(id)),
  createMailbox: (input: CreateMailboxRequest) =>
    api.post<Mailbox>(endpoints.mailboxes.collection, { body: input }),
  updateMailbox: (id: string, input: UpdateMailboxRequest) =>
    api.patch<Mailbox>(endpoints.mailboxes.byId(id), { body: input }),
  deleteMailbox: (id: string) => api.delete<null>(endpoints.mailboxes.byId(id)),
  setMailboxPassword: (id: string, password: string) =>
    api.post<null>(endpoints.mailboxes.password(id), { body: { password } }),
  mailboxQuota: (id: string) => api.get<QuotaUsage>(endpoints.mailboxes.quota(id)),
  mailboxLogins: (id: string, limit: number) =>
    fetchList<SaslLogin>(endpoints.mailboxes.logins(id), { limit }),

  listAppPasswords: (id: string) => fetchList<AppPassword>(endpoints.mailboxes.appPasswords(id)),
  createAppPassword: (id: string, input: CreateAppPasswordRequest) =>
    api.post<CreatedAppPassword>(endpoints.mailboxes.appPasswords(id), { body: input }),
  updateAppPassword: (id: string, appPasswordId: string, input: UpdateAppPasswordRequest) =>
    api.patch<AppPassword>(endpoints.mailboxes.appPassword(id, appPasswordId), { body: input }),
  deleteAppPassword: (id: string, appPasswordId: string) =>
    api.delete<null>(endpoints.mailboxes.appPassword(id, appPasswordId)),

  getSieve: (id: string) => api.get<MailboxSieve>(endpoints.mailboxes.sieve(id)),
  putSieve: (id: string, input: PutSieveRequest) =>
    api.put<MailboxSieve>(endpoints.mailboxes.sieve(id), { body: input }),
};

export const mailRoutingApi = {
  aliases: resourceApi<MailAlias, CreateAliasRequest, UpdateAliasRequest>(
    endpoints.mailRouting.aliases,
  ),
  spamAliases: resourceApi<SpamAlias, CreateSpamAliasRequest, UpdateSpamAliasRequest>(
    endpoints.mailRouting.spamAliases,
  ),
  senderAcl: resourceApi<SenderAcl, CreateSenderAclRequest, UpdateSenderAclRequest>(
    endpoints.mailRouting.senderAcl,
  ),
  relayhosts: resourceApi<Relayhost, CreateRelayhostRequest, UpdateRelayhostRequest>(
    endpoints.mailRouting.relayhosts,
  ),
  transports: resourceApi<Transport, CreateTransportRequest, UpdateTransportRequest>(
    endpoints.mailRouting.transports,
  ),
  tlsPolicies: resourceApi<TlsPolicy, CreateTlsPolicyRequest, UpdateTlsPolicyRequest>(
    endpoints.mailRouting.tlsPolicies,
  ),
  recipientMaps: resourceApi<RecipientMap, CreateRecipientMapRequest, UpdateRecipientMapRequest>(
    endpoints.mailRouting.recipientMaps,
  ),
  bccMaps: resourceApi<BccMap, CreateBccMapRequest, UpdateBccMapRequest>(
    endpoints.mailRouting.bccMaps,
  ),
};
