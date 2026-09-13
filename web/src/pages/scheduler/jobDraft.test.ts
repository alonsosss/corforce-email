import { afterEach, describe, expect, it } from 'vitest';
import { ApiError, ERROR_CODES } from '@/api/errors';
import type { SchedulerHandler, SchedulerMeta } from '@/api/scheduler';
import { getLocale, t } from '@/i18n';
import { formatBytes } from '@/lib/quota';
import {
  charLength,
  draftFromJob,
  emptyDraft,
  handlerOf,
  maxTimeoutSeconds,
  parseGoDurationSeconds,
  serverFieldErrors,
  tenantHandlers,
  timeZoneSuggestions,
  toCreateRequest,
  toUpdateRequest,
  utf8Length,
  validateDraft,
  type JobDraft,
} from './jobDraft';
import {
  jobFixture,
  platformHandlerFixture,
  schedulerMetaFixture as META,
  tenantHandlerFixture,
} from './metaFixture';
import { formatDurationMs, formatSeconds } from './schedulerFormat';

const draft = (extra: Partial<JobDraft> = {}): JobDraft => ({
  ...emptyDraft(META),
  name: 'Resumen',
  code: 'digest',
  handler: tenantHandlerFixture.name,
  job_type: 'cron',
  cron_expression: '0 7 * * *',
  ...extra,
});

const validate = (
  extra: Partial<JobDraft>,
  meta: SchedulerMeta = META,
  handler: SchedulerHandler | null = tenantHandlerFixture,
) => validateDraft(draft(extra), meta, 'create', handler);

const cronError = (cron: string, meta: SchedulerMeta = META) =>
  validate({ cron_expression: cron }, meta).cron_expression;

const withLimits = (limits: Partial<SchedulerMeta['limits']>): SchedulerMeta => ({
  ...META,
  limits: { ...META.limits, ...limits },
});

describe('validacion con las reglas de GET /scheduler/meta', () => {
  it('acepta los descriptores de la meta; el resto de expresiones las decide el servidor', () => {
    for (const cron of ['@daily', '@hourly', '0 7 * * *', '@every 1h30m', '@every 1m', 'x y']) {
      expect(cronError(cron), cron).toBeUndefined();
    }
  });

  it('rechaza un descriptor que la meta no publica', () => {
    expect(cronError('@yearly')).toBe(
      t('scheduler.validation.descriptor', { list: META.cron.descriptors.join(', ') }),
    );
  });

  it('exige a @every el minimo de la meta, truncado a segundos como el parser', () => {
    const tooShort = t('scheduler.validation.everyMin', {
      min: formatSeconds(META.cron.min_every_seconds),
    });
    for (const cron of ['@every 30s', '@every 59.9s', '@every -5m', '@every 0']) {
      expect(cronError(cron), cron).toBe(tooShort);
    }
    expect(
      cronError('@every 90s', { ...META, cron: { ...META.cron, min_every_seconds: 120 } }),
    ).toBe(t('scheduler.validation.everyMin', { min: formatSeconds(120) }));
    expect(cronError('@every pronto')).toBeUndefined();
  });

  it('mide la longitud de la expresion en bytes, como len() en Go', () => {
    expect(utf8Length('ñ')).toBe(2);
    expect(cronError('a'.repeat(META.cron.max_length))).toBeUndefined();
    expect(cronError('ñ'.repeat(51))).toBe(t('validation.maxLength', { n: META.cron.max_length }));
    expect(cronError('   ')).toBe(t('validation.required'));
  });

  it('valida la zona con el patron y la longitud de la meta', () => {
    const zoneError = (timezone: string, meta: SchedulerMeta = META) =>
      validate({ timezone }, meta).timezone;
    for (const zone of ['UTC', 'America/Lima', 'Etc/GMT+5', 'America/Argentina/Buenos_Aires']) {
      expect(zoneError(zone), zone).toBeUndefined();
    }
    expect(zoneError('+05:00')).toBe(t('scheduler.validation.timezoneFormat'));
    expect(zoneError('')).toBe(t('validation.required'));
    expect(zoneError(`A${'b'.repeat(META.timezone.max_length)}`)).toBe(
      t('validation.maxLength', { n: META.timezone.max_length }),
    );
    const unreadable = { ...META, timezone: { ...META.timezone, pattern: '(?P<zona>' } };
    expect(zoneError('+05:00', unreadable)).toBeUndefined();
  });

  it('el intervalo va de min_interval_minutes a max_interval_minutes de la meta', () => {
    const intervalError = (value: string, meta: SchedulerMeta = META) =>
      validate({ job_type: 'interval', interval_minutes: value }, meta).interval_minutes;
    const range = t('validation.range', { min: 1, max: META.limits.max_interval_minutes });
    expect(intervalError('15')).toBeUndefined();
    expect(intervalError(String(META.limits.max_interval_minutes))).toBeUndefined();
    for (const value of ['0', '1.5', '', 'x', String(META.limits.max_interval_minutes + 1)]) {
      expect(intervalError(value), value).toBe(range);
    }
    const narrow = withLimits({ min_interval_minutes: 5, max_interval_minutes: 60 });
    expect(intervalError('4', narrow)).toBe(t('validation.range', { min: 5, max: 60 }));
    expect(intervalError('61', narrow)).toBe(t('validation.range', { min: 5, max: 60 }));
  });

  it('los textos se miden en caracteres, ya recortados, con los topes de la meta', () => {
    const meta = withLimits({ max_name_length: 5, max_code_length: 3, max_description_length: 4 });
    expect(charLength('ñandú')).toBe(5);
    expect(validate({ name: ' ñandú ', code: 'abc', description: 'abcd' }, meta)).toMatchObject({
      name: undefined,
      code: undefined,
      description: undefined,
    });
    const errors = validate({ name: 'ñandús', code: 'abcd', description: 'abcde' }, meta);
    expect(errors.name).toBe(t('validation.maxLength', { n: 5 }));
    expect(errors.code).toBe(t('validation.maxLength', { n: 3 }));
    expect(errors.description).toBe(t('validation.maxLength', { n: 4 }));
    expect(validateDraft(draft({ code: 'abcd' }), meta, 'edit', null).code).toBeUndefined();
  });

  it('exige obligatorios, reintentos dentro del tope y un payload JSON; el codigo solo en el alta', () => {
    const errors = validate({
      name: ' ',
      code: '',
      handler: '',
      max_retries: '-1',
      payload: '{nope',
    });
    expect(errors.name).toBe(t('validation.required'));
    expect(errors.code).toBe(t('validation.required'));
    expect(errors.handler).toBe(t('validation.required'));
    expect(errors.max_retries).toBe(
      t('validation.range', { min: 0, max: META.limits.max_retries }),
    );
    expect(errors.payload).toBe(t('scheduler.validation.payloadJson'));
    expect(validate({ max_retries: String(META.limits.max_retries) }).max_retries).toBeUndefined();
    expect(validate({ max_retries: String(META.limits.max_retries + 1) }).max_retries).toBe(
      t('validation.range', { min: 0, max: META.limits.max_retries }),
    );
    expect(validateDraft(draft({ code: '' }), META, 'edit', null).code).toBeUndefined();
  });

  it('el payload no supera max_payload_bytes, medido en bytes UTF-8', () => {
    const meta = withLimits({ max_payload_bytes: 10 });
    // 10 bytes caben; 12 bytes no, aunque sean solo 7 caracteres.
    expect(validate({ payload: '"ññññ"' }, meta).payload).toBeUndefined();
    expect(validate({ payload: '"ñññññ"' }, meta).payload).toBe(
      t('scheduler.validation.payloadSize', { max: formatBytes(10) }),
    );
  });

  it('el plazo no supera el maximo del manejador elegido ni el tope general de la meta', () => {
    const handlerMax = tenantHandlerFixture.max_timeout_seconds;
    expect(maxTimeoutSeconds(META, tenantHandlerFixture)).toBe(handlerMax);
    expect(maxTimeoutSeconds(META, null)).toBe(META.limits.max_timeout_seconds);
    expect(validate({ timeout_seconds: '0' }).timeout_seconds).toBeUndefined();
    expect(validate({ timeout_seconds: String(handlerMax) }).timeout_seconds).toBeUndefined();
    expect(validate({ timeout_seconds: String(handlerMax + 1) }).timeout_seconds).toBe(
      t('scheduler.validation.timeoutHandlerMax', { value: formatSeconds(handlerMax) }),
    );
    const general = META.limits.max_timeout_seconds;
    const range = t('validation.range', { min: 0, max: general });
    expect(validate({ timeout_seconds: String(handlerMax + 1) }, META, null).timeout_seconds).toBe(
      undefined,
    );
    expect(validate({ timeout_seconds: String(general + 1) }, META, null).timeout_seconds).toBe(
      range,
    );
    expect(validate({ timeout_seconds: '-1' }).timeout_seconds).toBe(range);
  });

  it('lee duraciones de Go y deja al servidor las que no entiende', () => {
    expect(parseGoDurationSeconds('1h30m')).toBe(5400);
    expect(parseGoDurationSeconds('1.5h')).toBe(5400);
    expect(parseGoDurationSeconds('500ms')).toBe(0);
    expect(parseGoDurationSeconds('0')).toBe(0);
    for (const raw of ['', '5', '5 m', 'm5', '5x']) {
      expect(parseGoDurationSeconds(raw), raw).toBeNull();
    }
  });
});

describe('cuerpos del API', () => {
  it('el alta lleva todos los campos y anula los del tipo que no se eligio', () => {
    const body = toCreateRequest(
      draft({
        job_type: 'interval',
        interval_minutes: '15',
        cron_expression: '0 7 * * *',
        description: '  ',
        payload: ' {"a":1} ',
        timeout_seconds: '120',
      }),
      'interval',
    );
    expect(body).toEqual({
      name: 'Resumen',
      code: 'digest',
      description: null,
      job_type: 'interval',
      cron_expression: null,
      timezone: META.timezone.default,
      interval_minutes: 15,
      handler: tenantHandlerFixture.name,
      payload: '{"a":1}',
      max_retries: 0,
      timeout_seconds: 120,
    });
  });

  it('la edicion reproduce el trabajo guardado y no lleva code', () => {
    const job = jobFixture({ payload: '{"k":"v"}', description: 'Cada manana' });
    const body = toUpdateRequest(draftFromJob(job), job.job_type);
    expect(body).not.toHaveProperty('code');
    expect(body).toEqual({
      name: job.name,
      description: 'Cada manana',
      job_type: 'cron',
      cron_expression: job.cron_expression,
      timezone: job.timezone,
      interval_minutes: null,
      handler: job.handler,
      payload: '{"k":"v"}',
      max_retries: job.max_retries,
      timeout_seconds: job.timeout_seconds,
    });
  });
});

describe('errores del servidor por campo (error.details.field)', () => {
  const rejected = (status: number, code: string, message: string, field?: string) => {
    const error = { code, message, ...(field ? { details: { field } } : {}) };
    return new ApiError(status, error, { error });
  };
  const validation = (message: string, field?: string) =>
    rejected(422, ERROR_CODES.VALIDATION_ERROR, message, field);

  it('cada 422 va al campo que nombra el servidor, con su mensaje', () => {
    const message = 'invalid job: max_retries must be between 0 and 10';
    expect(serverFieldErrors(validation(message, 'max_retries'))).toEqual({
      max_retries: t('scheduler.form.serverRejected', { detail: message }),
    });
    const handler = 'handler not allowed: "x" is not in the catalog';
    expect(serverFieldErrors(validation(handler, 'handler'))).toEqual({
      handler: t('scheduler.form.serverRejected', { detail: handler }),
    });
  });

  it('la expresion y la zona llevan su propio texto', () => {
    const cron = 'invalid cron expression: expected exactly 5 fields, found 2: [0 7]';
    expect(serverFieldErrors(validation(cron, 'cron_expression'))).toEqual({
      cron_expression: t('scheduler.form.cronRejected', { detail: cron }),
    });
    const zone = 'invalid time zone: "Mars/Olympus" is not in the time zone database';
    expect(
      serverFieldErrors(rejected(422, ERROR_CODES.INVALID_TIMEZONE, zone, 'timezone')),
    ).toEqual({ timezone: t('scheduler.form.timezoneRejected', { detail: zone }) });
  });

  it('el 409 que nombra code es el codigo repetido', () => {
    expect(
      serverFieldErrors(rejected(409, ERROR_CODES.CONFLICT, 'job already exists', 'code')),
    ).toEqual({ code: t('scheduler.error.codeTaken') });
    expect(serverFieldErrors(rejected(409, ERROR_CODES.CONFLICT, 'job is locked'))).toEqual({});
  });

  it('sin campo, o con uno que no esta en el formulario, queda como error general', () => {
    expect(serverFieldErrors(validation('invalid cron expression: x'))).toEqual({});
    expect(serverFieldErrors(validation('is_active must be true or false', 'is_active'))).toEqual(
      {},
    );
    expect(serverFieldErrors(rejected(500, ERROR_CODES.INTERNAL_ERROR, 'x', 'name'))).toEqual({});
    expect(serverFieldErrors(new Error('invalid cron expression: x'))).toEqual({});
    expect(serverFieldErrors(null)).toEqual({});
  });
});

describe('catalogo de manejadores', () => {
  const catalog = [tenantHandlerFixture, platformHandlerFixture];

  it('solo ofrece a una empresa los manejadores de alcance tenant', () => {
    expect(tenantHandlers(catalog)).toEqual([tenantHandlerFixture]);
    expect(tenantHandlers([platformHandlerFixture])).toEqual([]);
  });

  it('resuelve el manejador de un trabajo segun su alcance', () => {
    expect(handlerOf(catalog, jobFixture())).toBe(tenantHandlerFixture);
    expect(handlerOf(catalog, jobFixture({ handler: platformHandlerFixture.name }))).toBeNull();
    expect(
      handlerOf(catalog, jobFixture({ tenant_id: null, handler: platformHandlerFixture.name })),
    ).toBe(platformHandlerFixture);
    expect(handlerOf(catalog, jobFixture({ handler: 'retirado.handler' }))).toBeNull();
  });
});

describe('zonas sugeridas por el navegador', () => {
  const intl = Intl as unknown as Record<string, unknown>;
  const original = intl.supportedValuesOf;

  afterEach(() => {
    Object.defineProperty(Intl, 'supportedValuesOf', {
      value: original,
      configurable: true,
      writable: true,
    });
  });

  it('son solo sugerencias y siempre incluyen la zona por defecto de la meta', () => {
    Object.defineProperty(Intl, 'supportedValuesOf', {
      value: () => ['America/Lima', 'Europe/Madrid'],
      configurable: true,
      writable: true,
    });
    expect(timeZoneSuggestions('UTC')).toEqual(['UTC', 'America/Lima', 'Europe/Madrid']);
    expect(timeZoneSuggestions('Europe/Madrid')).toEqual(['America/Lima', 'Europe/Madrid']);
  });

  it('sin Intl.supportedValuesOf queda solo la zona por defecto', () => {
    Object.defineProperty(Intl, 'supportedValuesOf', {
      value: undefined,
      configurable: true,
      writable: true,
    });
    expect(timeZoneSuggestions('UTC')).toEqual(['UTC']);
  });
});

describe('formato de plazos y duraciones', () => {
  const one = new Intl.NumberFormat(getLocale(), { maximumFractionDigits: 1 });

  it('los topes en la unidad exacta mas grande', () => {
    expect(formatSeconds(60)).toBe(t('scheduler.unit.min', { n: 1 }));
    expect(formatSeconds(7200)).toBe(t('scheduler.unit.h', { n: 2 }));
    expect(formatSeconds(604_800)).toBe(t('scheduler.unit.d', { n: 7 }));
    expect(formatSeconds(90)).toBe(t('scheduler.unit.s', { n: one.format(90) }));
  });

  it('duration_ms del servicio', () => {
    expect(formatDurationMs(null)).toBe(t('common.dash'));
    expect(formatDurationMs(250)).toBe(t('scheduler.duration.ms', { n: one.format(250) }));
    expect(formatDurationMs(1500)).toBe(t('scheduler.unit.s', { n: one.format(1.5) }));
    expect(formatDurationMs(125_000)).toBe(t('scheduler.duration.minSec', { m: 2, s: 5 }));
    expect(formatDurationMs(3_720_000)).toBe(t('scheduler.duration.hourMin', { h: 1, m: 2 }));
  });
});
