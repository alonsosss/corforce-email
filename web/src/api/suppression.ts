import { api } from './client';
import { endpoints } from './endpoints';
import { fetchPage } from './paging';
import { cachedResource } from './resource';
import type { Page, PageQuery } from './types';

// DTOs de services/suppression/internal/adapters/http/handler.go, domain/entities.go y
// app.Stats. Motivos, cuales se pueden retirar y los topes llegan en GET /suppression/meta.

export type SuppressionReason = 'complaint' | 'hard_bounce' | 'unsubscribe' | 'invalid' | 'manual';

/** Una causa de exclusion (domain.Entry): una fila por (empresa, direccion, causa). */
export interface SuppressionCause {
  id: string;
  tenant_id: string;
  email: string;
  reason: SuppressionReason;
  source: string;
  detail: string;
  message_id?: string;
  campaign_id?: string;
  expires_at?: string;
  created_at: string;
  updated_at: string;
}

/**
 * Direccion excluida con todas sus causas (domain.Address): los campos de primer nivel son
 * los de la causa principal (la vigente mas grave), `reasons` las causas vigentes de mas a
 * menos grave y `causes` todas las filas, tambien una manual caducada, cada una con el id
 * con que se retira.
 */
export interface SuppressionEntry extends SuppressionCause {
  reasons: SuppressionReason[] | null;
  causes: SuppressionCause[] | null;
}

export interface SuppressionQuery extends PageQuery {
  reason?: SuppressionReason;
  search?: string;
}

/** Por el API publico solo se registran exclusiones manuales. */
export interface CreateSuppressionRequest {
  email: string;
  detail: string;
  expires_at?: string;
}

export interface ImportSuppressionRequest {
  emails: string[];
  detail: string;
}

export interface ImportResult {
  id: string;
  total: number;
  added: number;
  skipped: number;
}

export interface SuppressionImport {
  id: string;
  tenant_id: string;
  total: number;
  added: number;
  skipped: number;
  created_by: string;
  created_at: string;
}

/**
 * Respuesta de POST /suppression/check por direccion: `reason` es la causa vigente mas
 * grave y `reasons` todas las vigentes, de mas a menos grave.
 */
export interface SuppressedAddress {
  email: string;
  reason: SuppressionReason;
  reasons: SuppressionReason[] | null;
}

export interface SuppressionStats {
  total: number;
  by_reason: Partial<Record<SuppressionReason, number>> | null;
}

export interface ReasonInfo {
  reason: SuppressionReason;
  severity: number;
  /** Una baja pedida por la persona solo la levanta un consentimiento nuevo. */
  removable: boolean;
}

/** GET /suppression/meta: motivos de mas a menos grave y topes del API. */
export interface SuppressionMeta {
  reasons: ReasonInfo[];
  manual_reasons: SuppressionReason[];
  max_check_emails: number;
  max_import_emails: number;
  max_per_page: number;
  max_email_length: number;
  max_detail_length: number;
}

export function isRemovable(meta: SuppressionMeta, reason: SuppressionReason): boolean {
  return meta.reasons.find((r) => r.reason === reason)?.removable ?? false;
}

export const suppressionApi = {
  list: (query: SuppressionQuery): Promise<Page<SuppressionEntry>> =>
    fetchPage<SuppressionEntry>(endpoints.suppression.entries.collection, { ...query }),
  create: (input: CreateSuppressionRequest) =>
    api.post<SuppressionEntry>(endpoints.suppression.entries.collection, { body: input }),
  remove: (id: string) => api.delete<null>(endpoints.suppression.entries.byId(id)),

  importEmails: (input: ImportSuppressionRequest) =>
    api.post<ImportResult>(endpoints.suppression.import, { body: input }),
  listImports: (query: PageQuery): Promise<Page<SuppressionImport>> =>
    fetchPage<SuppressionImport>(endpoints.suppression.imports, { ...query }),

  /** Consulta de lectura: el gateway la gatea como GET aunque viaje por POST. */
  check: async (emails: string[]): Promise<SuppressedAddress[]> => {
    const res = await api.post<{ suppressed: SuppressedAddress[] | null }>(
      endpoints.suppression.check,
      { body: { emails } },
    );
    return res.data?.suppressed ?? [];
  },
  stats: () => api.get<SuppressionStats>(endpoints.suppression.stats),

  meta: async (): Promise<SuppressionMeta> =>
    (await api.get<SuppressionMeta>(endpoints.suppression.meta)).data,
};

export const suppressionMeta = cachedResource(suppressionApi.meta);
