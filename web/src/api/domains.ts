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
export type DnsRecordKind = 'ownership_txt' | 'mx' | 'spf' | 'dkim' | 'dkim_previous' | 'dmarc';
export type VerifyOutcome = 'verified' | 'failed' | 'inconclusive';

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

export interface RotateDkimResult extends ManagedDomain {
  dns_record: DnsRecord;
  grace_until: string;
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
};
