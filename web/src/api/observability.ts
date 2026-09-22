import { api } from './client';
import { endpoints } from './endpoints';

// DTOs de services/observability/internal/domain/logs.go. La lista de servicios consultables la sirve
// el API (lista blanca fija del dominio): la web nunca la conoce de antemano ni compone LogQL.

/** Tope de lineas de una consulta (domain.MaxLogLimit). */
export const LOGS_MAX_LIMIT = 500;
/** Largo maximo del texto buscado, en caracteres (domain.MaxLogTextRunes). */
export const LOGS_MAX_TEXT = 200;
/** Ventana maxima en horas (domain.MaxLogWindow). */
export const LOGS_MAX_WINDOW_HOURS = 24;

export type LogDirection = 'backward' | 'forward';
export const LOG_DIRECTIONS: readonly LogDirection[] = ['backward', 'forward'];

export interface LogEntry {
  /** RFC 3339 con nanosegundos. */
  timestamp: string;
  line: string;
  /** Etiquetas del flujo: contenedor, plano y flujo (stdout o stderr). */
  labels: Record<string, string>;
}

export interface LogPage {
  service: string;
  since: string;
  until: string;
  direction: LogDirection;
  limit: number;
  /** Se alcanzo el limite: puede haber mas lineas en la ventana. */
  truncated: boolean;
  entries: LogEntry[];
}

export interface LogQueryParams {
  service: string;
  q?: string;
  /** RFC 3339. Por defecto la ultima hora hasta ahora. */
  since?: string;
  until?: string;
  limit?: number;
  direction?: LogDirection;
}

export const observabilityApi = {
  /** Servicios consultables, ordenados: solo el superadmin (permiso observability/logs, de plataforma). */
  listLogServices: async (signal?: AbortSignal) =>
    (await api.get<{ services: string[] }>(endpoints.observability.logServices, { signal })).data
      .services,
  queryLogs: async (params: LogQueryParams, signal?: AbortSignal) =>
    (await api.get<LogPage>(endpoints.observability.logs, { params: { ...params }, signal })).data,
};
