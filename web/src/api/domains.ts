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

export interface DomainDetail extends DomainWithRecords {
  dns_checks: DnsCheck[];
}

export interface VerifyResult extends DomainDetail {
  outcome: VerifyOutcome;
  integration_errors: string[];
}

export interface RotateDkimResult extends ManagedDomain {
  dns_record: DnsRecord;
  grace_until: string;
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
};
