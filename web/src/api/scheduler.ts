import { api } from './client';
import { endpoints } from './endpoints';
import { fetchList, fetchPage } from './paging';
import { cachedResource } from './resource';
import type { Page, PageQuery } from './types';

// DTOs de services/scheduler/internal/adapters/http/dto.go (jobDTO, executionDTO, taskDTO,
// handlerDTO), meta.go (metaResponse) y los cuerpos de handler.go (createJobReq,
// updateJobReq). El decodificador del servicio rechaza campos desconocidos: los cuerpos
// llevan exactamente estos campos.

/**
 * Tipos de trabajo de domain/entities.go (JobTypeCron, JobTypeInterval, JobTypeOneTime).
 * GET /scheduler/meta no los publica; el formulario los necesita porque cada tipo tiene sus
 * propios campos.
 */
export const JOB_TYPES = ['cron', 'interval', 'one_time'] as const;
export type JobType = (typeof JOB_TYPES)[number];

export type ExecutionStatus = 'pending' | 'running' | 'completed' | 'failed' | 'cancelled';
export type FailureReason = 'executor' | 'timeout' | 'handler_not_allowed';
export type HandlerScope = 'tenant' | 'platform';

/** Alcance de los trabajos que se crean por el API: siempre de la empresa que llama. */
export const TENANT_SCOPE: HandlerScope = 'tenant';
/** Alcance de los trabajos de plataforma (tenant_id null), que una empresa solo lee. */
export const PLATFORM_SCOPE: HandlerScope = 'platform';

export interface SchedulerJob {
  id: string;
  /** null en un trabajo de plataforma: se lee, pero no se cambia desde una empresa. */
  tenant_id: string | null;
  name: string;
  code: string;
  description: string | null;
  job_type: JobType;
  cron_expression: string | null;
  timezone: string;
  interval_minutes: number | null;
  handler: string;
  /** Documento JSON en texto, tal como lo guarda el servicio. */
  payload: string | null;
  is_active: boolean;
  max_retries: number;
  /** 0 toma el maximo del manejador. */
  timeout_seconds: number;
  created_at: string;
  updated_at: string;
}

export interface JobExecution {
  id: string;
  job_id: string;
  tenant_id: string | null;
  status: ExecutionStatus;
  started_at: string | null;
  completed_at: string | null;
  duration_ms: number | null;
  result: string | null;
  error_message: string | null;
  /** 0 en el primer intento. */
  retry_count: number;
  created_at: string;
  deadline_at: string | null;
  next_attempt_at: string | null;
  retry_of: string | null;
  failure_reason: FailureReason | null;
}

export interface ScheduledTask {
  id: string;
  tenant_id: string;
  name: string;
  description: string | null;
  trigger_at: string;
  handler: string;
  payload: string | null;
  status: string;
  executed_at: string | null;
  created_at: string;
}

export interface SchedulerHandler {
  name: string;
  service: string;
  description: string;
  max_timeout_seconds: number;
  scopes: HandlerScope[];
}

/** GET /scheduler/meta: reglas de domain/timezone.go y domain/cron.go. */
export interface SchedulerMeta {
  timezone: {
    default: string;
    format: string;
    /** Expresion regular (sintaxis de Go) de un nombre de zona valido. */
    pattern: string;
    max_length: number;
  };
  cron: {
    /** Descriptores admitidos ademas de "@every <duracion>". */
    descriptors: string[];
    /** Periodo minimo de @every; tambien la resolucion de interval_minutes. */
    min_every_seconds: number;
    max_length: number;
  };
}

export interface CreateJobRequest {
  name: string;
  code: string;
  description: string | null;
  job_type: JobType;
  cron_expression: string | null;
  timezone: string;
  interval_minutes: number | null;
  handler: string;
  payload: string | null;
  max_retries: number;
  timeout_seconds: number;
}

/** PUT reemplaza la definicion entera; el codigo no se cambia. */
export type UpdateJobRequest = Omit<CreateJobRequest, 'code'>;

export interface JobListQuery {
  is_active?: boolean;
}

export const schedulerApi = {
  meta: async (): Promise<SchedulerMeta> =>
    (await api.get<SchedulerMeta>(endpoints.scheduler.meta)).data,
  handlers: (): Promise<SchedulerHandler[]> =>
    fetchList<SchedulerHandler>(endpoints.scheduler.handlers),

  /** El servicio devuelve la lista completa de la empresa, sin paginar. */
  listJobs: (query: JobListQuery): Promise<SchedulerJob[]> =>
    fetchList<SchedulerJob>(endpoints.scheduler.jobs.collection, { is_active: query.is_active }),
  getJob: (id: string) => api.get<SchedulerJob>(endpoints.scheduler.jobs.byId(id)),
  createJob: (input: CreateJobRequest) =>
    api.post<SchedulerJob>(endpoints.scheduler.jobs.collection, { body: input }),
  updateJob: (id: string, input: UpdateJobRequest) =>
    api.put<SchedulerJob>(endpoints.scheduler.jobs.byId(id), { body: input }),
  enableJob: (id: string) => api.post<null>(endpoints.scheduler.enable(id)),
  disableJob: (id: string) => api.post<null>(endpoints.scheduler.disable(id)),
  runJob: (id: string) => api.post<JobExecution>(endpoints.scheduler.run(id)),

  history: (id: string, query: PageQuery): Promise<Page<JobExecution>> =>
    fetchPage<JobExecution>(endpoints.scheduler.history(id), { ...query }),
  cancelExecution: (id: string) => api.post<null>(endpoints.scheduler.cancelExecution(id)),
  retryExecution: (id: string) => api.post<JobExecution>(endpoints.scheduler.retryExecution(id)),

  listPendingTasks: (): Promise<ScheduledTask[]> =>
    fetchList<ScheduledTask>(endpoints.scheduler.tasks.collection),
  cancelTask: (id: string) => api.post<null>(endpoints.scheduler.cancelTask(id)),
};

/** Reglas de los trabajos, una peticion por sesion. */
export const schedulerMeta = cachedResource(() => schedulerApi.meta());

/** Catalogo de manejadores (lista blanca handlers.json del servicio), una peticion por sesion. */
export const schedulerHandlers = cachedResource(() => schedulerApi.handlers());
