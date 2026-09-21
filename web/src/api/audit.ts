import { api } from './client';
import { endpoints } from './endpoints';
import { toPage, type Page, type PageQuery } from './types';

// DTOs de services/audit/internal/adapters/http/handler.go y domain/entities.go.

export type Severity = 'info' | 'warning' | 'critical';
export type RiskLevel = 'low' | 'medium' | 'high' | 'critical';

export const SEVERITIES: readonly Severity[] = ['info', 'warning', 'critical'];
export const RISK_LEVELS: readonly RiskLevel[] = ['low', 'medium', 'high', 'critical'];

export interface AuditLog {
  id: string;
  tenant_id: string;
  user_id: string;
  session_id?: string;
  action: string;
  module: string;
  resource: string;
  resource_id?: string;
  ip_address: string;
  user_agent?: string;
  request_id?: string;
  before?: string;
  after?: string;
  changes?: string;
  severity: Severity;
  created_at: string;
}

export interface AuditLogQuery extends PageQuery {
  user_id?: string;
  module?: string;
  resource?: string;
  action?: string;
  severity?: string;
  date_from?: string;
  date_to?: string;
  ip_address?: string;
}

export interface SecurityEvent {
  id: string;
  tenant_id: string;
  user_id?: string;
  event_type: string;
  ip_address: string;
  user_agent?: string;
  detail: string;
  risk_level: RiskLevel;
  acknowledged: boolean;
  acknowledged_by?: string;
  acknowledged_at?: string;
  created_at: string;
}

export interface SecurityEventQuery extends PageQuery {
  event_type?: string;
  risk_level?: string;
  date_from?: string;
  date_to?: string;
  acknowledged?: boolean;
}

export interface ChainHead {
  seq: number;
  hash: string;
  hash_version: number;
}

export interface ChainAnchor {
  head_seq: number;
  head_hash: string;
  hash_version: number;
  anchored_at: string;
}

/** Veredicto del conjunto: filas de las dos cadenas y sus anclas. `chain` y `reason` dicen
 * cual fallo primero y por que; `security_events` es el resultado de la segunda cadena. */
export interface ChainIntegrity {
  ok: boolean;
  checked: number;
  chain: string;
  reason?: string;
  broken_id?: string;
  broken_seq?: number;
  broken_hash_version?: number;
  versions?: Record<string, number>;
  head?: ChainHead;
  anchor?: ChainAnchor;
  security_events?: ChainIntegrity;
}

interface StatusResponse {
  status: string;
}

const DEFAULT_PAGE = { page: 1, per_page: 20 } as const;

export const auditApi = {
  searchLogs: async (query: AuditLogQuery): Promise<Page<AuditLog>> =>
    toPage(await api.get<AuditLog[]>(endpoints.audit.logs, { params: { ...query } }), {
      page: query.page ?? DEFAULT_PAGE.page,
      per_page: query.per_page ?? DEFAULT_PAGE.per_page,
    }),

  listSecurityEvents: async (query: SecurityEventQuery): Promise<Page<SecurityEvent>> =>
    toPage(
      await api.get<SecurityEvent[]>(endpoints.audit.securityEvents, { params: { ...query } }),
      {
        page: query.page ?? DEFAULT_PAGE.page,
        per_page: query.per_page ?? DEFAULT_PAGE.per_page,
      },
    ),
  acknowledge: (id: string) => api.post<StatusResponse>(endpoints.audit.acknowledge(id)),

  integrity: () => api.get<ChainIntegrity>(endpoints.audit.integrity),
};
