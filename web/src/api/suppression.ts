import { api } from './client';
import { endpoints } from './endpoints';
import { fetchPage } from './paging';
import type { Page, PageQuery } from './types';

// DTOs de services/suppression/internal/adapters/http/handler.go, domain/entities.go y
// app.Stats.

export type SuppressionReason = 'complaint' | 'hard_bounce' | 'unsubscribe' | 'invalid' | 'manual';

/** Espejo de domain.Reasons(): de mas a menos grave. */
export const SUPPRESSION_REASONS: readonly SuppressionReason[] = [
  'complaint',
  'hard_bounce',
  'unsubscribe',
  'invalid',
  'manual',
];

// Espejo de app.MaxImportEmails y app.MaxCheckEmails, y del tope de per_page.
export const SUPPRESSION_MAX_IMPORT = 10_000;
export const SUPPRESSION_MAX_CHECK = 1_000;
export const SUPPRESSION_MAX_PAGE_SIZE = 100;

/**
 * Espejo de domain.Reason.Removable(): una baja pedida por la persona solo la levanta un
 * consentimiento nuevo, nunca un operador (el backend responde 409 UNSUBSCRIBE_PROTECTED).
 */
export function isRemovableReason(reason: SuppressionReason): boolean {
  return reason !== 'unsubscribe';
}

export interface SuppressionEntry {
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

export interface SuppressedAddress {
  email: string;
  reason: SuppressionReason;
}

export interface SuppressionStats {
  total: number;
  by_reason: Partial<Record<SuppressionReason, number>> | null;
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
};
