import { afterEach, describe, expect, it } from 'vitest';
import { ApiError, ERROR_CODES } from '@/api/errors';
import type { SchedulerMeta } from '@/api/scheduler';
import { getLocale, t } from '@/i18n';
import {
  draftFromJob,
  emptyDraft,
  handlerOf,
  minIntervalMinutes,
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

const cronError = (cron: string, meta: SchedulerMeta = META) =>
  validateDraft(draft({ cron_expression: cron }), meta, 'create').cron_expression;

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
      validateDraft(draft({ timezone }), meta, 'create').timezone;
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

  it('el intervalo minimo sale de la resolucion que publica la meta', () => {
    expect(minIntervalMinutes(META)).toBe(1);
    expect(minIntervalMinutes({ ...META, cron: { ...META.cron, min_every_seconds: 90 } })).toBe(2);
    const intervalError = (value: string) =>
      validateDraft(draft({ job_type: 'interval', interval_minutes: value }), META, 'create')
        .interval_minutes;
    expect(intervalError('15')).toBeUndefined();
    for (const value of ['0', '1.5', '', 'x']) {
      expect(intervalError(value), value).toBe(t('scheduler.validation.intervalMin', { n: 1 }));
    }
  });

  it('exige obligatorios, enteros no negativos y un payload JSON; el codigo solo en el alta', () => {
    const errors = validateDraft(
      draft({ name: ' ', code: '', handler: '', max_retries: '-1', payload: '{nope' }),
      META,
      'create',
    );
    expect(errors.name).toBe(t('validation.required'));
    expect(errors.code).toBe(t('validation.required'));
    expect(errors.handler).toBe(t('validation.required'));
    expect(errors.max_retries).toBe(t('validation.nonNegativeInteger'));
    expect(errors.payload).toBe(t('scheduler.validation.payloadJson'));
    expect(validateDraft(draft({ code: '' }), META, 'edit').code).toBeUndefined();
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

describe('errores del servidor por campo', () => {
  const validation = (message: string) =>
    new ApiError(422, { code: ERROR_CODES.VALIDATION_ERROR, message });

  it('la expresion invalida va al campo cron con el detalle del servicio', () => {
    expect(
      serverFieldErrors(
        validation('invalid cron expression: expected exactly 5 fields, found 2: [0 7]'),
      ),
    ).toEqual({
      cron_expression: t('scheduler.form.cronRejected', {
        detail: 'expected exactly 5 fields, found 2: [0 7]',
      }),
    });
  });

  it('INVALID_TIMEZONE va a la zona', () => {
    const err = new ApiError(422, {
      code: ERROR_CODES.INVALID_TIMEZONE,
      message: 'invalid time zone: "Mars/Olympus" is not in the time zone database',
    });
    expect(serverFieldErrors(err)).toEqual({
      timezone: t('scheduler.form.timezoneRejected', {
        detail: '"Mars/Olympus" is not in the time zone database',
      }),
    });
  });

  it('un manejador fuera del catalogo va al manejador', () => {
    expect(serverFieldErrors(validation('handler not allowed: "x" is not in the catalog'))).toEqual(
      { handler: t('scheduler.error.handlerNotAllowed') },
    );
  });

  it('separa los obligatorios de pkg/validate y los invalid job de cada campo', () => {
    expect(serverFieldErrors(validation('name is required; handler is required'))).toEqual({
      name: t('validation.required'),
      handler: t('validation.required'),
    });
    expect(
      serverFieldErrors(
        validation('invalid job: interval_minutes must be positive for interval jobs'),
      ),
    ).toEqual({
      interval_minutes: t('scheduler.form.serverRejected', {
        detail: 'interval_minutes must be positive for interval jobs',
      }),
    });
    expect(
      Object.keys(
        serverFieldErrors(validation('invalid job: payload must be a valid JSON document')),
      ),
    ).toEqual(['payload']);
  });

  it('el 409 del alta es el codigo repetido', () => {
    expect(
      serverFieldErrors(
        new ApiError(409, { code: ERROR_CODES.CONFLICT, message: 'job already exists' }),
      ),
    ).toEqual({ code: t('scheduler.error.codeTaken') });
  });

  it('lo que no nombra un campo no se asocia a ninguno', () => {
    expect(serverFieldErrors(validation('something unexpected'))).toEqual({});
    expect(serverFieldErrors(new ApiError(500, { code: 'INTERNAL_ERROR', message: 'x' }))).toEqual(
      {},
    );
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
