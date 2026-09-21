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

export type IntegrityRunStatus = 'running' | 'completed' | 'cancelled' | 'failed';
export type IntegrityRunMode = 'full' | 'incremental';
export type IntegrityRunPhase = 'audit_logs' | 'security_events' | 'anchors' | 'done';

/** Verificacion de la cadena en segundo plano (services/audit/internal/domain/integrity_run.go).
 * `result` solo esta en las terminadas; `error` es el codigo de un fallo tecnico, que no dice
 * nada de la cadena. `current_seq` y `target_seq` son la posicion alcanzada en audit_logs y la de
 * su cabeza al empezar. */
export interface IntegrityRun {
  id: string;
  mode: IntegrityRunMode;
  trigger: 'manual' | 'sweep' | 'request';
  requested_by?: string;
  status: IntegrityRunStatus;
  phase: IntegrityRunPhase;
  checked: number;
  current_seq: number;
  target_seq: number;
  cancel_requested: boolean;
  result?: ChainIntegrity;
  error?: string;
  started_at: string;
  heartbeat_at: string;
  finished_at?: string;
}

/** Codigo con el que audit rechaza lanzar una verificacion cuando la empresa ya tiene una en
 * curso; `error.details.run_id` es esa verificacion. */
export const VERIFICATION_RUNNING = 'VERIFICATION_RUNNING';

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

  startIntegrityRun: (mode: IntegrityRunMode) =>
    api.post<IntegrityRun>(endpoints.audit.integrityRuns, { body: { mode } }),
  listIntegrityRuns: (signal?: AbortSignal) =>
    api.get<IntegrityRun[]>(endpoints.audit.integrityRuns, { signal }),
  integrityRun: (id: string, signal?: AbortSignal) =>
    api.get<IntegrityRun>(endpoints.audit.integrityRun(id), { signal }),
  cancelIntegrityRun: (id: string) =>
    api.post<IntegrityRun>(endpoints.audit.cancelIntegrityRun(id)),
};
