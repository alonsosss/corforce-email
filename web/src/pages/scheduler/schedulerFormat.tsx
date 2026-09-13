import type { ExecutionStatus, JobType, SchedulerJob } from '@/api/scheduler';
import { Badge, type BadgeTone } from '@/design/components';
import { getLocale, t, tEnum } from '@/i18n';

export function executionStatusTone(status: ExecutionStatus): BadgeTone {
  if (status === 'running') return 'accent';
  if (status === 'pending') return 'info';
  if (status === 'completed') return 'success';
  if (status === 'failed') return 'danger';
  return 'neutral';
}

export function ExecutionStatusBadge({ status }: { status: ExecutionStatus }) {
  return (
    <Badge tone={executionStatusTone(status)}>{tEnum('scheduler.executionStatus', status)}</Badge>
  );
}

export function JobTypeBadge({ type }: { type: JobType }) {
  return <Badge tone="info">{tEnum('scheduler.jobType', type)}</Badge>;
}

/** Cuando se lanza: la expresion con su zona, el intervalo o una sola vez. */
export function ScheduleLabel({ job }: { job: SchedulerJob }) {
  if (job.job_type === 'cron') {
    return (
      <span className="cf-cell-stack">
        <span className="cf-mono cf-break">{job.cron_expression ?? t('common.dash')}</span>
        <span className="cf-text-muted cf-text-sm">{job.timezone}</span>
      </span>
    );
  }
  if (job.job_type === 'interval') {
    return job.interval_minutes === null ? (
      <>{t('common.dash')}</>
    ) : (
      <>{t('scheduler.schedule.interval', { n: job.interval_minutes })}</>
    );
  }
  return <>{t('scheduler.schedule.oneTime')}</>;
}

const number = () => new Intl.NumberFormat(getLocale(), { maximumFractionDigits: 1 });

/** Un tope o un plazo en segundos, en la unidad exacta mas grande: 60 -> "1 min". */
export function formatSeconds(seconds: number): string {
  const n = Math.trunc(seconds);
  if (n > 0 && n % 86_400 === 0) return t('scheduler.unit.d', { n: n / 86_400 });
  if (n > 0 && n % 3_600 === 0) return t('scheduler.unit.h', { n: n / 3_600 });
  if (n > 0 && n % 60 === 0) return t('scheduler.unit.min', { n: n / 60 });
  return t('scheduler.unit.s', { n: number().format(n) });
}

/** Duracion de una ejecucion (duration_ms del servicio). */
export function formatDurationMs(ms: number | null): string {
  if (ms === null || !Number.isFinite(ms) || ms < 0) return t('common.dash');
  if (ms < 1_000) return t('scheduler.duration.ms', { n: number().format(Math.round(ms)) });
  if (ms < 60_000) {
    return t('scheduler.unit.s', { n: number().format(Math.floor(ms / 100) / 10) });
  }
  const seconds = Math.floor(ms / 1_000);
  if (seconds < 3_600) {
    return t('scheduler.duration.minSec', { m: Math.floor(seconds / 60), s: seconds % 60 });
  }
  const minutes = Math.floor(seconds / 60);
  if (minutes < 1_440) {
    return t('scheduler.duration.hourMin', { h: Math.floor(minutes / 60), m: minutes % 60 });
  }
  return t('scheduler.duration.dayHour', {
    d: Math.floor(minutes / 1_440),
    h: Math.floor((minutes % 1_440) / 60),
  });
}

/** El payload con sangria si es JSON; si no, tal cual lo guardo el servicio. */
export function prettyPayload(payload: string): string {
  try {
    return JSON.stringify(JSON.parse(payload), null, 2);
  } catch {
    return payload;
  }
}
