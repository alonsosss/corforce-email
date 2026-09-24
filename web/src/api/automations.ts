import { api } from './client';
import { endpoints } from './endpoints';
import { fetchPage } from './paging';
import { cachedResource } from './resource';
import type { TemplateKind } from './templates';
import type { Page, PageQuery } from './types';

// DTOs de services/automations: domain/workflow.go (Workflow, Trigger), domain/steps.go
// (Step), domain/run.go (Run), domain/doi.go (DOISettings, DOIDelivery) y las peticiones
// de internal/adapters/http/handler.go. Disparadores, pasos, estados y topes llegan en
// GET /automations/meta (adapters/http/meta.go).

export type WorkflowStatus = 'draft' | 'active' | 'paused' | 'archived';
export type StepType = 'wait' | 'send_email' | 'add_to_list' | 'remove_from_list' | 'branch';
export type ConditionKind = 'email_opened' | 'email_clicked' | 'segment' | 'attribute';
export type RunStatus = 'waiting' | 'running' | 'completed' | 'failed' | 'cancelled' | 'skipped';
export type DoiStatus = 'pending' | 'sent' | 'skipped' | 'failed';

export interface WorkflowTrigger {
  type: string;
  /** Solo en los disparadores con campaign_filter del catalogo. */
  campaign_id?: string;
  /** Solo en los disparadores con date: atributo de fecha, hora local y zona de respaldo. */
  attribute?: string;
  hour?: number;
  timezone?: string;
}

/** Condicion de un paso branch (domain/condition.go). Cada tipo usa solo sus campos. */
export interface WorkflowCondition {
  kind: ConditionKind;
  /** email_opened y email_clicked: id del paso send_email cuyo correo se mira. */
  step?: string;
  segment_id?: string;
  /** attribute: clave, operador y valor del DSL de segmentos de contacts. */
  attribute?: string;
  op?: string;
  value?: unknown;
}

/**
 * Cada tipo usa solo sus campos; el servicio rechaza los que no le corresponden. El flujo es
 * un grafo sin ciclos (domain/graph.go): el primer paso es la entrada, next el siguiente y
 * en una rama then y else; un destino ausente es el fin.
 */
export interface WorkflowStep {
  id?: string;
  type: StepType;
  next?: string;
  then?: string;
  else?: string;
  condition?: WorkflowCondition;
  /** wait: entero y unidad del catalogo (30m, 12h, 3d). */
  duration?: string;
  template_id?: string;
  /** Se fija al activar si no se indica. */
  template_version?: number;
  from_email?: string;
  from_name?: string;
  reply_to?: string;
  list_id?: string;
}

export interface Workflow {
  id: string;
  tenant_id: string;
  name: string;
  description: string;
  status: WorkflowStatus;
  trigger: WorkflowTrigger;
  list_id: string | null;
  re_entry: boolean;
  steps: WorkflowStep[];
  /** manual, o "CODIGO: detalle" cuando el ejecutor lo pauso. */
  pause_reason: string;
  created_by: string;
  activated_at: string | null;
  created_at: string;
  updated_at: string;
}

export interface WorkflowRun {
  id: string;
  tenant_id: string;
  workflow_id: string;
  contact_id: string;
  trigger_event_id: string;
  /** Indice del paso en curso o del ultimo, desde 0. */
  step_index: number;
  status: RunStatus;
  next_run_at: string;
  attempts: number;
  error_code: string;
  last_error: string;
  finished_at: string | null;
  created_at: string;
  updated_at: string;
}

export interface DoiSettings {
  tenant_id: string;
  enabled: boolean;
  template_id: string | null;
  from_email: string;
  from_name: string;
  reply_to: string;
  updated_by?: string;
  updated_at?: string;
}

/** Historial del doble opt-in. El enlace de confirmacion es una credencial: no se guarda. */
export interface DoiDelivery {
  id: string;
  tenant_id: string;
  event_id: string;
  contact_id: string;
  status: DoiStatus;
  reason: string;
  message_id: string | null;
  attempts: number;
  template_id: string | null;
  from_email: string;
  sent_at: string | null;
  created_at: string;
  updated_at: string;
}

export interface WorkflowRequest {
  name: string;
  description: string;
  trigger: WorkflowTrigger;
  list_id: string | null;
  re_entry: boolean;
  steps: WorkflowStep[];
}

export interface DoiSettingsRequest {
  enabled: boolean;
  template_id: string | null;
  from_email: string;
  from_name: string;
  reply_to: string;
}

export interface WorkflowQuery extends PageQuery {
  status?: WorkflowStatus;
  search?: string;
}

export interface RunQuery extends PageQuery {
  status?: RunStatus;
}

export interface DeliveryQuery extends PageQuery {
  status?: DoiStatus;
}

/** Acciones que admite un flujo en cada estado, segun el dominio. */
export interface WorkflowStatusInfo {
  status: WorkflowStatus;
  editable: boolean;
  can_activate: boolean;
  can_pause: boolean;
  can_archive: boolean;
  deletable: boolean;
}

export interface TriggerInfo {
  type: string;
  campaign_filter: boolean;
  date: boolean;
}

export interface ConditionInfo {
  kind: ConditionKind;
  /** La condicion mira el correo de un paso send_email anterior. */
  step: boolean;
}

export interface WaitUnit {
  unit: string;
  seconds: number;
}

/** GET /automations/meta. */
export interface AutomationsMeta {
  statuses: WorkflowStatusInfo[];
  trigger_types: TriggerInfo[];
  step_types: StepType[];
  condition_kinds: ConditionInfo[];
  wait: { units: WaitUnit[]; min_seconds: number; max_seconds: number };
  run_statuses: RunStatus[];
  doi_statuses: DoiStatus[];
  pause_reason_manual: string;
  /** Tipo de plantilla que exige cada uso. */
  template_kinds: { send_email: TemplateKind; double_opt_in: TemplateKind };
  /** Tope efectivo de correos del doble opt-in por contacto. */
  doi_limits: { per_day: number; per_30_days: number };
  limits: {
    min_steps: number;
    max_steps: number;
    max_name_length: number;
    max_description_length: number;
    max_pause_reason_length: number;
    max_search_length: number;
    max_depth: number;
    max_step_id_length: number;
    max_trigger_hour: number;
    max_condition_value_bytes: number;
  };
  pagination: { default_page_size: number; max_page_size: number };
}

export function workflowStatusInfo(
  meta: AutomationsMeta,
  status: WorkflowStatus,
): WorkflowStatusInfo | null {
  return meta.statuses.find((s) => s.status === status) ?? null;
}

export const automationsApi = {
  listWorkflows: (query: WorkflowQuery): Promise<Page<Workflow>> =>
    fetchPage<Workflow>(endpoints.automations.workflows.collection, { ...query }),
  getWorkflow: (id: string) => api.get<Workflow>(endpoints.automations.workflows.byId(id)),
  createWorkflow: (input: WorkflowRequest) =>
    api.post<Workflow>(endpoints.automations.workflows.collection, { body: input }),
  /** PATCH: list_id null quita el filtro; un campo ausente no cambia. */
  updateWorkflow: (id: string, input: Partial<WorkflowRequest>) =>
    api.patch<Workflow>(endpoints.automations.workflows.byId(id), { body: input }),
  deleteWorkflow: (id: string) => api.delete<null>(endpoints.automations.workflows.byId(id)),
  activate: (id: string) => api.post<Workflow>(endpoints.automations.activate(id)),
  pause: (id: string, reason: string) =>
    api.post<Workflow>(endpoints.automations.pause(id), {
      body: reason ? { reason } : undefined,
    }),
  archive: (id: string) => api.post<Workflow>(endpoints.automations.archive(id)),

  listRuns: (workflowId: string, query: RunQuery): Promise<Page<WorkflowRun>> =>
    fetchPage<WorkflowRun>(endpoints.automations.runs(workflowId), { ...query }),
  getRun: (id: string) => api.get<WorkflowRun>(endpoints.automations.run(id)),

  getDoiSettings: () => api.get<DoiSettings>(endpoints.automations.doubleOptIn),
  putDoiSettings: (input: DoiSettingsRequest) =>
    api.put<DoiSettings>(endpoints.automations.doubleOptIn, { body: input }),
  listDeliveries: (query: DeliveryQuery): Promise<Page<DoiDelivery>> =>
    fetchPage<DoiDelivery>(endpoints.automations.deliveries, { ...query }),

  meta: async (): Promise<AutomationsMeta> =>
    (await api.get<AutomationsMeta>(endpoints.automations.meta)).data,
};

/** Catalogo de automatizaciones compartido por todas las pantallas de la sesion. */
export const automationsMeta = cachedResource(automationsApi.meta);
