import { api } from './client';
import { endpoints } from './endpoints';
import { fetchPage } from './paging';
import type { Page, PageQuery } from './types';

// DTOs de services/domain-service/internal/adapters/http/handler.go (domainResponse,
// checksResponse) y domain/entities.go. La clave privada DKIM nunca sale del servicio: la
// publica solo viaja como valor del TXT que hay que publicar.

export type DomainPurpose = 'corporate' | 'sending' | 'both';
export type DomainStatus = 'pending' | 'verified' | 'failed' | 'disabled';
export type DmarcPolicy = 'none' | 'quarantine' | 'reject';
export type DnsRecordKind =
  | 'ownership_txt'
  | 'mx'
  | 'spf'
  | 'dkim'
  | 'dkim_previous'
  | 'dmarc'
  | 'mta_sts'
  | 'tls_rpt'
  | 'ses_mail_from_mx'
  | 'ses_mail_from_spf';
export type VerifyOutcome = 'verified' | 'failed' | 'inconclusive';
/** Estado de la identidad del dominio en Amazon SES; solo verified permite enviar por SES. */
export type SesIdentityStatus = 'pending' | 'verified' | 'failed';
export type SesCheckStatus = 'pending' | 'success' | 'failed' | 'temporary_failure' | 'not_started';

/** Como se publica el DNS del dominio: a mano o por la plataforma en el proveedor conectado. */
export type DnsProvider = 'cloudflare';
export type DnsMode = 'manual' | DnsProvider;
export const DNS_MODE_MANUAL: DnsMode = 'manual';
export const DNS_PROVIDER_CLOUDFLARE: DnsProvider = 'cloudflare';

export const DOMAIN_PURPOSES: readonly DomainPurpose[] = ['corporate', 'sending', 'both'];
export const DMARC_POLICIES: readonly DmarcPolicy[] = ['none', 'quarantine', 'reject'];

export interface ManagedDomain {
  id: string;
  domain: string;
  purpose: DomainPurpose;
  status: DomainStatus;
  verification_token: string;
  verified_at: string | null;
  last_checked_at: string | null;
  dkim_selector: string;
  dkim_public_key: string;
  dkim_key_bits: number;
  dkim_previous_selector: string | null;
  dkim_rotated_at: string | null;
  /** Hasta cuando, como pronto, debe seguir publicado el TXT de la clave anterior. */
  dkim_previous_until: string | null;
  /** Una revocacion sigue sin confirmar en los servidores de correo de la celda. */
  dkim_revocation_pending: boolean;
  dmarc_policy: DmarcPolicy;
  dns_mode: DnsMode;
  /** Ultima publicacion automatica completa en el proveedor. */
  dns_published_at: string | null;
  /** Identidad en Amazon SES de un dominio de envio; null si no la tiene. */
  ses_identity_status: SesIdentityStatus | null;
  ses_dkim_status: SesCheckStatus | null;
  ses_mail_from_status: SesCheckStatus | null;
  ses_checked_at: string | null;
  /** Ultimo fallo al sincronizar con SES; se reintenta en el barrido. */
  ses_last_error: string | null;
  created_at: string;
  updated_at: string;
}

/** Registro que el cliente debe publicar en su DNS (domain.DNSRecord). */
export interface DnsRecord {
  record: DnsRecordKind;
  type: string;
  host: string;
  value: string;
  required: boolean;
}

/** Ultimo resultado de comprobacion de un registro. */
export interface DnsCheck {
  record: DnsRecordKind;
  checked_at: string;
  expected: string;
  observed: string;
  ok: boolean;
  detail: string;
}

export interface DomainWithRecords extends ManagedDomain {
  dns_records: DnsRecord[];
}

export type DkimRotationKind = 'scheduled' | 'compromised';

/** Entrada del historial de claves DKIM (rotationResponse). */
export interface DkimRotation {
  id: string;
  kind: DkimRotationKind;
  selector: string;
  previous_selector: string | null;
  revoked_selectors: string[];
  reason: string;
  actor_id: string | null;
  rotated_at: string;
}

export interface DomainDetail extends DomainWithRecords {
  dns_checks: DnsCheck[];
  dkim_rotations: DkimRotation[];
}

/** La verificacion no devuelve el historial de claves: se funde con la ficha ya cargada. */
export interface VerifyResult extends DomainWithRecords {
  dns_checks: DnsCheck[];
  outcome: VerifyOutcome;
  integration_errors: string[];
}

export type DnsRecordAction = 'unchanged' | 'created' | 'updated' | 'replaced' | 'conflict' | 'failed';

/** Lo que una publicacion hizo con un registro (publicationResponse en handler Go dns.go). */
export interface DnsPublicationRecord {
  record: DnsRecordKind;
  type: string;
  host: string;
  value: string;
  action: DnsRecordAction;
  /** Valores del cliente en conflicto o reemplazados. */
  existing: string[];
  error_code?: string;
}

export interface DnsPublication {
  provider: DnsProvider;
  zone: string;
  published_at: string;
  complete: boolean;
  records: DnsPublicationRecord[];
  /** TXT de la plataforma retirados (claves DKIM revocadas o fuera de gracia). */
  removed: string[];
  /** Nombres con TXT del cliente que la plataforma no toca. */
  kept: string[];
}

/** La verificacion que sigue a la publicacion; outcome es null si no pudo completarse. */
export interface PublishDnsResult extends DomainWithRecords {
  dns_publication: DnsPublication;
  outcome: VerifyOutcome | null;
  dns_checks: DnsCheck[];
  integration_errors: string[];
}

/** Publicacion automatica tras rotar o revocar claves DKIM de un dominio en modo automatico. */
export interface DnsAutomation {
  provider: DnsProvider;
  publication: DnsPublication | null;
  error_code: string | null;
}

/** Conexion de la empresa con el proveedor. El token nunca viaja de vuelta: solo su pista. */
export interface DnsProviderStatus {
  provider: DnsProvider;
  connected: boolean;
  token_hint?: string;
  zones?: string[];
  zones_visible?: number;
  connected_by?: string | null;
  connected_at?: string;
  last_validated_at?: string;
}

export interface DnsProviderDisconnectResult extends DnsProviderStatus {
  disconnected: boolean;
  domains_reset: number;
}

export interface RotateDkimResult extends ManagedDomain {
  dns_record: DnsRecord;
  grace_until: string;
  dns_automation?: DnsAutomation;
}

/** current_selector es el selector actual que se vio: repetir la peticion no genera otra clave. */
export interface RevokeDkimRequest {
  current_selector: string;
  reason: string;
}

export interface RevokeDkimResult extends ManagedDomain {
  dns_record: DnsRecord;
  /** TXT que el cliente debe retirar de su DNS ya; solo llevan host y tipo. */
  remove_dns_records: DnsRecord[];
  revocation: DkimRotation;
  /** Los servidores de correo ya no tienen ninguna clave revocada. */
  engines_retired: boolean;
  integration_errors: string[];
  dns_automation?: DnsAutomation;
}

export interface CreateDomainRequest {
  domain: string;
  purpose: DomainPurpose;
  dmarc_policy?: DmarcPolicy;
}

export interface UpdateDomainRequest {
  purpose?: DomainPurpose;
  dmarc_policy?: DmarcPolicy;
}

export const domainsApi = {
  list: (query: PageQuery): Promise<Page<ManagedDomain>> =>
    fetchPage<ManagedDomain>(endpoints.domains.collection, { ...query }),
  get: (id: string) => api.get<DomainDetail>(endpoints.domains.byId(id)),
  create: (input: CreateDomainRequest) =>
    api.post<DomainWithRecords>(endpoints.domains.collection, { body: input }),
  update: (id: string, input: UpdateDomainRequest) =>
    api.patch<DomainWithRecords>(endpoints.domains.byId(id), { body: input }),
  remove: (id: string) => api.delete<null>(endpoints.domains.byId(id)),
  verify: (id: string) => api.post<VerifyResult>(endpoints.domains.verify(id)),
  rotateDkim: (id: string) => api.post<RotateDkimResult>(endpoints.domains.rotateDkim(id)),
  revokeDkim: (id: string, input: RevokeDkimRequest) =>
    api.post<RevokeDkimResult>(endpoints.domains.revokeDkim(id), { body: input }),
  setDnsMode: (id: string, mode: DnsMode) =>
    api.post<DomainWithRecords>(endpoints.domains.dnsMode(id), { body: { mode } }),
  /** replace son los tipos de registro cuyos valores del cliente se confirma reemplazar. */
  publishDns: (id: string, replace: DnsRecordKind[] = []) =>
    api.post<PublishDnsResult>(endpoints.domains.publishDns(id), { body: { replace } }),
};

export const dnsProvidersApi = {
  status: (provider: DnsProvider) =>
    api.get<DnsProviderStatus>(endpoints.domains.dnsProvider(provider)),
  /** Conectar guarda una credencial de terceros: el backend puede pedir step-up. */
  connect: (provider: DnsProvider, apiToken: string) =>
    api.post<DnsProviderStatus>(endpoints.domains.dnsProviderConnect(provider), {
      body: { api_token: apiToken },
    }),
  disconnect: (provider: DnsProvider) =>
    api.post<DnsProviderDisconnectResult>(endpoints.domains.dnsProviderDisconnect(provider)),
};
