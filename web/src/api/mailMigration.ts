import { api } from './client';
import { endpoints } from './endpoints';
import { fetchPage } from './paging';
import type { Page } from './types';

// DTOs de services/mail-migration (API de administracion). Las opciones del formulario y los
// topes llegan en GET /mail-migration/meta: la interfaz no las copia. El trabajo nunca lleva
// la contrasena de origen; solo viaja en el POST de creacion.

export type MigrationTls = string;
export type MigrationStatus = 'pending' | 'running' | 'succeeded' | 'failed' | 'cancelled';
export type MigrationPhase = '' | 'initial' | 'catchup';

export interface MigrationMeta {
  /** false: falta la clave del ejecutor y el servicio no acepta trabajos (crear responde 503). */
  configured: boolean;
  source_ports: number[];
  source_tls_modes: MigrationTls[];
  /** Puerto (como texto) a modo de TLS habitual en ese puerto. */
  default_tls_for_port: Record<string, MigrationTls>;
  max_active_jobs: number;
  active_jobs: number;
  phases: MigrationPhase[];
}

export interface MigrationFolderProgress {
  name: string;
  messages_copied: number;
  messages_skipped: number;
  messages_failed: number;
}

export interface MigrationProgress {
  folders_total: number;
  folders_done: number;
  messages_total: number;
  messages_copied: number;
  messages_skipped: number;
  messages_failed: number;
  bytes_copied: number;
  folders: MigrationFolderProgress[];
}

export interface MigrationLastError {
  code: string;
  message: string;
}

export interface MigrationJob {
  id: string;
  mailbox_id: string;
  mailbox_username: string;
  source_host: string;
  source_port: number;
  source_tls: MigrationTls;
  source_username: string;
  status: MigrationStatus;
  phase: MigrationPhase;
  progress: MigrationProgress;
  attempt: number;
  last_error: MigrationLastError | null;
  requested_by: string;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
  cancel_requested_at: string | null;
  heartbeat_at: string | null;
}

/** Cuerpo de POST /mail-migration/jobs. El decodificador del servicio rechaza campos desconocidos. */
export interface CreateMigrationRequest {
  mailbox_id: string;
  source_host: string;
  source_port: number;
  source_tls: MigrationTls;
  source_username: string;
  source_password: string;
}

/** Un slice vacio de Go llega como null. */
function normalizeJob(job: MigrationJob): MigrationJob {
  return { ...job, progress: { ...job.progress, folders: job.progress.folders ?? [] } };
}

export const mailMigrationApi = {
  meta: async (): Promise<MigrationMeta> =>
    (await api.get<MigrationMeta>(endpoints.mailMigration.meta)).data,

  listJobs: async (mailboxId: string, perPage: number): Promise<Page<MigrationJob>> => {
    const page = await fetchPage<MigrationJob>(endpoints.mailMigration.jobs, {
      mailbox_id: mailboxId,
      page: 1,
      per_page: perPage,
    });
    return { ...page, items: page.items.map(normalizeJob) };
  },

  getJob: async (id: string): Promise<MigrationJob> =>
    normalizeJob((await api.get<MigrationJob>(endpoints.mailMigration.job(id))).data),

  createJob: async (input: CreateMigrationRequest): Promise<MigrationJob> =>
    normalizeJob(
      (await api.post<MigrationJob>(endpoints.mailMigration.jobs, { body: input })).data,
    ),

  cancelJob: async (id: string): Promise<MigrationJob> =>
    normalizeJob((await api.post<MigrationJob>(endpoints.mailMigration.cancel(id))).data),
};
