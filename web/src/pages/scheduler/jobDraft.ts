import { ERROR_CODES, errorDetail, isApiError } from '@/api/errors';
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
import { t, type MessageKey } from '@/i18n';
import { formatBytes } from '@/lib/quota';
import { type FieldErrors } from '@/lib/validate';
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

/** Longitud en bytes UTF-8: el servicio mide con len() de Go. */
export function utf8Length(value: string): number {
  let bytes = 0;
  for (const char of value) {
    const cp = char.codePointAt(0) ?? 0;
    bytes += cp < 0x80 ? 1 : cp < 0x800 ? 2 : cp < 0x10000 ? 3 : 4;
  }
  return bytes;
}

/** Longitud en caracteres (puntos de codigo): el servicio mide con utf8.RuneCountInString. */
export function charLength(value: string): number {
  return Array.from(value).length;
}

/** Plazo maximo que admite el trabajo: el del manejador, dentro del tope general de la meta. */
export function maxTimeoutSeconds(meta: SchedulerMeta, handler: SchedulerHandler | null): number {
  const general = meta.limits.max_timeout_seconds;
  return handler ? Math.min(handler.max_timeout_seconds, general) : general;
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

/** Texto que el cuerpo lleva recortado: se mide tal como viaja. */
function textError(value: string, max: number, required: boolean): string | undefined {
  const text = value.trim();
  if (required && !text) return t('validation.required');
  return charLength(text) > max ? t('validation.maxLength', { n: max }) : undefined;
}

function integerError(value: string, min: number, max: number): string | undefined {
  const n = Number(value);
  return value.trim() !== '' && Number.isSafeInteger(n) && n >= min && n <= max
    ? undefined
    : t('validation.range', { min, max });
}

function cronError(value: string, meta: SchedulerMeta): string | undefined {
  const spec = value.trim();
  if (!spec) return t('validation.required');
  if (utf8Length(spec) > meta.cron.max_length) {
    return t('validation.maxLength', { n: meta.cron.max_length });
  }
  if (!spec.startsWith('@') || meta.cron.descriptors.includes(spec)) return undefined;
  if (spec.startsWith(EVERY_PREFIX)) {
    const seconds = parseGoDurationSeconds(spec.slice(EVERY_PREFIX.length));
    return seconds !== null && seconds < meta.cron.min_every_seconds
      ? t('scheduler.validation.everyMin', { min: formatSeconds(meta.cron.min_every_seconds) })
      : undefined;
  }
  return t('scheduler.validation.descriptor', { list: meta.cron.descriptors.join(', ') });
}

function timezoneError(value: string, meta: SchedulerMeta): string | undefined {
  const zone = value.trim();
  if (!zone) return t('validation.required');
  if (utf8Length(zone) > meta.timezone.max_length) {
    return t('validation.maxLength', { n: meta.timezone.max_length });
  }
  const pattern = timezonePattern(meta);
  return pattern && !pattern.test(zone) ? t('scheduler.validation.timezoneFormat') : undefined;
}

function timeoutError(
  value: string,
  meta: SchedulerMeta,
  handler: SchedulerHandler | null,
): string | undefined {
  const range = integerError(value, 0, meta.limits.max_timeout_seconds);
  if (range) return range;
  const max = maxTimeoutSeconds(meta, handler);
  return Number(value) > max
    ? t('scheduler.validation.timeoutHandlerMax', { value: formatSeconds(max) })
    : undefined;
}

function payloadError(value: string, maxBytes: number): string | undefined {
  const text = value.trim();
  if (!text) return undefined;
  if (utf8Length(text) > maxBytes) {
    return t('scheduler.validation.payloadSize', { max: formatBytes(maxBytes) });
  }
  try {
    JSON.parse(text);
    return undefined;
  } catch {
    return t('scheduler.validation.payloadJson');
  }
}

/**
 * Comprueba lo que la meta y el manejador elegido permiten comprobar antes de enviar; el resto
 * lo decide el servidor. handler es null si el trabajo apunta a uno fuera del catalogo.
 */
export function validateDraft(
  draft: JobDraft,
  meta: SchedulerMeta,
  mode: DraftMode,
  handler: SchedulerHandler | null,
): JobErrors {
  const { limits } = meta;
  const errors: JobErrors = {
    name: textError(draft.name, limits.max_name_length, true),
    code: mode === 'create' ? textError(draft.code, limits.max_code_length, true) : undefined,
    description: textError(draft.description, limits.max_description_length, false),
    handler: draft.handler ? undefined : t('validation.required'),
    job_type: draft.job_type ? undefined : t('validation.required'),
    timezone: timezoneError(draft.timezone, meta),
    max_retries: integerError(draft.max_retries, 0, limits.max_retries),
    timeout_seconds: timeoutError(draft.timeout_seconds, meta, handler),
    payload: payloadError(draft.payload, limits.max_payload_bytes),
  };
  if (draft.job_type === 'cron') {
    errors.cron_expression = cronError(draft.cron_expression, meta);
  }
  if (draft.job_type === 'interval') {
    errors.interval_minutes = integerError(
      draft.interval_minutes,
      limits.min_interval_minutes,
      limits.max_interval_minutes,
    );
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

/** Clave de error.details con la que el scheduler nombra el campo que fallo. */
const FIELD_DETAIL = 'field';

const FORM_FIELDS: ReadonlySet<string> = new Set<JobField>([
  'name',
  'code',
  'description',
  'handler',
  'job_type',
  'cron_expression',
  'timezone',
  'interval_minutes',
  'max_retries',
  'timeout_seconds',
  'payload',
]);

const isFormField = (field: string): field is JobField => FORM_FIELDS.has(field);

const REJECTED: Partial<Record<JobField, MessageKey>> = {
  cron_expression: 'scheduler.form.cronRejected',
  timezone: 'scheduler.form.timezoneRejected',
};

/**
 * Error del servidor asociado al campo que nombra error.details.field. Vacio si no nombra un
 * campo del formulario: entonces se muestra como error general.
 */
export function serverFieldErrors(err: unknown): JobErrors {
  const field = errorDetail(err, FIELD_DETAIL);
  if (!isApiError(err) || !field || !isFormField(field)) return {};
  // El unico 409 con campo es el codigo repetido del alta (ErrJobAlreadyExists).
  if (err.code === ERROR_CODES.CONFLICT) {
    return field === 'code' ? { code: t('scheduler.error.codeTaken') } : {};
  }
  if (err.code !== ERROR_CODES.VALIDATION_ERROR && err.code !== ERROR_CODES.INVALID_TIMEZONE) {
    return {};
  }
  return {
    [field]: t(REJECTED[field] ?? 'scheduler.form.serverRejected', { detail: err.message }),
  };
}
