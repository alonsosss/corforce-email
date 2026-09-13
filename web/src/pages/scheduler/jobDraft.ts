import { ERROR_CODES, isApiError } from '@/api/errors';
import {
  PLATFORM_SCOPE,
  TENANT_SCOPE,
  type CreateJobRequest,
  type JobType,
  type SchedulerHandler,
  type SchedulerJob,
  type SchedulerMeta,
  type UpdateJobRequest,
} from '@/api/scheduler';
import { t } from '@/i18n';
import { rules, type FieldErrors } from '@/lib/validate';
import { formatSeconds } from './schedulerFormat';

/** Estado del formulario de un trabajo: lo que se escribe, antes de convertirlo al cuerpo. */
export interface JobDraft {
  name: string;
  code: string;
  description: string;
  handler: string;
  job_type: JobType | '';
  cron_expression: string;
  timezone: string;
  interval_minutes: string;
  max_retries: string;
  timeout_seconds: string;
  payload: string;
}

export type JobField = keyof JobDraft;
export type JobErrors = FieldErrors<JobField>;
export type DraftMode = 'create' | 'edit';

export function isPlatformJob(job: Pick<SchedulerJob, 'tenant_id'>): boolean {
  return job.tenant_id === null;
}

/** Manejadores a los que puede apuntar un trabajo de la empresa. */
export function tenantHandlers(handlers: readonly SchedulerHandler[]): SchedulerHandler[] {
  return handlers.filter((h) => h.scopes.includes(TENANT_SCOPE));
}

/** El manejador del trabajo en el catalogo, si sigue en el y acepta su alcance. */
export function handlerOf(
  handlers: readonly SchedulerHandler[],
  job: Pick<SchedulerJob, 'handler' | 'tenant_id'>,
): SchedulerHandler | null {
  const scope = isPlatformJob(job) ? PLATFORM_SCOPE : TENANT_SCOPE;
  return handlers.find((h) => h.name === job.handler && h.scopes.includes(scope)) ?? null;
}

export function emptyDraft(meta: SchedulerMeta): JobDraft {
  return {
    name: '',
    code: '',
    description: '',
    handler: '',
    job_type: '',
    cron_expression: '',
    timezone: meta.timezone.default,
    interval_minutes: '',
    // Los ceros son lo que el servicio aplica a un campo ausente: sin reintentos y el
    // plazo maximo del manejador.
    max_retries: '0',
    timeout_seconds: '0',
    payload: '',
  };
}

export function draftFromJob(job: SchedulerJob): JobDraft {
  return {
    name: job.name,
    code: job.code,
    description: job.description ?? '',
    handler: job.handler,
    job_type: job.job_type,
    cron_expression: job.cron_expression ?? '',
    timezone: job.timezone,
    interval_minutes: job.interval_minutes === null ? '' : String(job.interval_minutes),
    max_retries: String(job.max_retries),
    timeout_seconds: String(job.timeout_seconds),
    payload: job.payload ?? '',
  };
}

/** interval_minutes minimo: la resolucion del planificador que publica la meta, en minutos. */
export function minIntervalMinutes(meta: SchedulerMeta): number {
  return Math.max(1, Math.ceil(meta.cron.min_every_seconds / 60));
}

/** Longitud en bytes UTF-8: el servicio mide con len() de Go. */
export function utf8Length(value: string): number {
  let bytes = 0;
  for (const char of value) {
    const cp = char.codePointAt(0) ?? 0;
    bytes += cp < 0x80 ? 1 : cp < 0x800 ? 2 : cp < 0x10000 ? 3 : 4;
  }
  return bytes;
}

const EVERY_PREFIX = '@every ';
const DURATION_PART = /(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|μs|ms|s|m|h)/g;
const UNIT_SECONDS: Record<string, number> = {
  ns: 1e-9,
  us: 1e-6,
  µs: 1e-6,
  μs: 1e-6,
  ms: 1e-3,
  s: 1,
  m: 60,
  h: 3600,
};

/**
 * Segundos de una duracion de Go (time.ParseDuration), truncados como hace el parser de
 * @every. null si no se puede leer: entonces decide el servidor.
 */
export function parseGoDurationSeconds(raw: string): number | null {
  let rest = raw;
  let sign = 1;
  if (rest.startsWith('-') || rest.startsWith('+')) {
    sign = rest.startsWith('-') ? -1 : 1;
    rest = rest.slice(1);
  }
  if (rest === '0') return 0;
  let total = 0;
  let consumed = 0;
  for (const match of rest.matchAll(DURATION_PART)) {
    const unit = UNIT_SECONDS[match[2] ?? ''];
    if (match.index !== consumed || unit === undefined) return null;
    total += Number(match[1]) * unit;
    consumed += match[0].length;
  }
  if (consumed === 0 || consumed !== rest.length) return null;
  return Math.trunc(sign * total);
}

/** La expresion regular de la meta, o null si este navegador no la compila. */
export function timezonePattern(meta: SchedulerMeta): RegExp | null {
  try {
    return new RegExp(meta.timezone.pattern);
  } catch {
    return null;
  }
}

/** Zonas que ofrece el navegador, solo como sugerencia: el servidor es quien decide. */
export function timeZoneSuggestions(fallback: string): string[] {
  const intl = Intl as unknown as { supportedValuesOf?: (key: 'timeZone') => string[] };
  let zones: string[] = [];
  try {
    zones = intl.supportedValuesOf?.('timeZone') ?? [];
  } catch {
    zones = [];
  }
  return zones.includes(fallback) ? zones : [fallback, ...zones];
}

function cronError(value: string, meta: SchedulerMeta): string | null {
  const spec = value.trim();
  if (!spec) return t('validation.required');
  if (utf8Length(spec) > meta.cron.max_length) {
    return t('validation.maxLength', { n: meta.cron.max_length });
  }
  if (!spec.startsWith('@') || meta.cron.descriptors.includes(spec)) return null;
  if (spec.startsWith(EVERY_PREFIX)) {
    const seconds = parseGoDurationSeconds(spec.slice(EVERY_PREFIX.length));
    return seconds !== null && seconds < meta.cron.min_every_seconds
      ? t('scheduler.validation.everyMin', { min: formatSeconds(meta.cron.min_every_seconds) })
      : null;
  }
  return t('scheduler.validation.descriptor', { list: meta.cron.descriptors.join(', ') });
}

function timezoneError(value: string, meta: SchedulerMeta): string | null {
  const zone = value.trim();
  if (!zone) return t('validation.required');
  if (utf8Length(zone) > meta.timezone.max_length) {
    return t('validation.maxLength', { n: meta.timezone.max_length });
  }
  const pattern = timezonePattern(meta);
  return pattern && !pattern.test(zone) ? t('scheduler.validation.timezoneFormat') : null;
}

function intervalError(value: string, meta: SchedulerMeta): string | null {
  const min = minIntervalMinutes(meta);
  const n = Number(value);
  return value.trim() !== '' && Number.isSafeInteger(n) && n >= min
    ? null
    : t('scheduler.validation.intervalMin', { n: min });
}

function payloadError(value: string): string | null {
  const text = value.trim();
  if (!text) return null;
  try {
    JSON.parse(text);
    return null;
  } catch {
    return t('scheduler.validation.payloadJson');
  }
}

/** Comprueba lo que la meta permite comprobar antes de enviar; el resto lo decide el servidor. */
export function validateDraft(draft: JobDraft, meta: SchedulerMeta, mode: DraftMode): JobErrors {
  const errors: JobErrors = {
    name: rules.required(draft.name) ?? undefined,
    code: mode === 'create' ? (rules.required(draft.code) ?? undefined) : undefined,
    handler: draft.handler ? undefined : t('validation.required'),
    job_type: draft.job_type ? undefined : t('validation.required'),
    timezone: timezoneError(draft.timezone, meta) ?? undefined,
    max_retries: rules.nonNegativeInteger(draft.max_retries) ?? undefined,
    timeout_seconds: rules.nonNegativeInteger(draft.timeout_seconds) ?? undefined,
    payload: payloadError(draft.payload) ?? undefined,
  };
  if (draft.job_type === 'cron') {
    errors.cron_expression = cronError(draft.cron_expression, meta) ?? undefined;
  }
  if (draft.job_type === 'interval') {
    errors.interval_minutes = intervalError(draft.interval_minutes, meta) ?? undefined;
  }
  return errors;
}

function toUpdateBody(draft: JobDraft, jobType: JobType): UpdateJobRequest {
  return {
    name: draft.name.trim(),
    description: draft.description.trim() || null,
    job_type: jobType,
    cron_expression: jobType === 'cron' ? draft.cron_expression.trim() : null,
    timezone: draft.timezone.trim(),
    interval_minutes: jobType === 'interval' ? Number(draft.interval_minutes) : null,
    handler: draft.handler,
    payload: draft.payload.trim() || null,
    max_retries: Number(draft.max_retries),
    timeout_seconds: Number(draft.timeout_seconds),
  };
}

/** Cuerpo del alta. Solo se llama con un borrador que pasa validateDraft. */
export function toCreateRequest(draft: JobDraft, jobType: JobType): CreateJobRequest {
  return { ...toUpdateBody(draft, jobType), code: draft.code.trim() };
}

/** Cuerpo de la edicion: sin code, que el servicio rechazaria como campo desconocido. */
export function toUpdateRequest(draft: JobDraft, jobType: JobType): UpdateJobRequest {
  return toUpdateBody(draft, jobType);
}

// El scheduler responde 422 VALIDATION_ERROR sin campo en error.details: el mensaje empieza
// por el error del dominio (domain/errors.go) o por "<campo> is required" de pkg/validate,
// varios unidos por "; ". Esos prefijos son el contrato que se puede leer.
const REQUIRED = /^(name|code|job_type|handler) is required$/;
const INVALID_JOB =
  /^invalid job: (job_type|interval_minutes|max_retries|timeout_seconds|payload)\b/;
const CRON_PREFIX = 'invalid cron expression';
const HANDLER_PREFIX = 'handler not allowed';
const TIMEZONE_PREFIX = 'invalid time zone';
const JOB_PREFIX = 'invalid job';

function detailAfter(message: string, prefix: string): string {
  const detail = message.slice(prefix.length).replace(/^:\s*/, '').trim();
  return detail || message;
}

/** Errores del servidor asociados a su campo; vacio si el error no es de un campo. */
export function serverFieldErrors(err: unknown): JobErrors {
  if (!isApiError(err)) return {};
  if (err.code === ERROR_CODES.INVALID_TIMEZONE) {
    return {
      timezone: t('scheduler.form.timezoneRejected', {
        detail: detailAfter(err.message, TIMEZONE_PREFIX),
      }),
    };
  }
  // El alta solo responde 409 cuando el codigo ya existe (ErrJobAlreadyExists).
  if (err.code === ERROR_CODES.CONFLICT) return { code: t('scheduler.error.codeTaken') };
  if (err.code !== ERROR_CODES.VALIDATION_ERROR) return {};
  const errors: JobErrors = {};
  for (const part of err.message.split(';')) {
    const message = part.trim();
    const required = REQUIRED.exec(message)?.[1] as JobField | undefined;
    if (required) {
      errors[required] = t('validation.required');
    } else if (message.startsWith(CRON_PREFIX)) {
      errors.cron_expression = t('scheduler.form.cronRejected', {
        detail: detailAfter(message, CRON_PREFIX),
      });
    } else if (message.startsWith(HANDLER_PREFIX)) {
      errors.handler = t('scheduler.error.handlerNotAllowed');
    } else {
      const field = INVALID_JOB.exec(message)?.[1] as JobField | undefined;
      if (field) {
        errors[field] = t('scheduler.form.serverRejected', {
          detail: detailAfter(message, JOB_PREFIX),
        });
      }
    }
  }
  return errors;
}
