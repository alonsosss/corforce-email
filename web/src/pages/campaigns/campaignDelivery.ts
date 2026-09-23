import type {
  ABCriterion,
  ABTest,
  ABTestInput,
  Campaign,
  CampaignPhase,
  CampaignPlan,
  CampaignsMeta,
  Engagement,
  Resend,
} from '@/api/campaigns';
import { t } from '@/i18n';

// Borradores y lecturas de la prueba A/B, el reenvio y el envio por zona horaria. Los topes
// salen de GET /campaigns/meta; el servicio vuelve a validar todo.

const MINUTES_PER_HOUR = 60;

export interface VariantDraft {
  subject: string;
  /** '' = la plantilla de la campana. */
  templateId: string;
  /** '' = la version publicada al programar o iniciar. */
  version: string;
}

export interface ABDraft {
  enabled: boolean;
  criterion: ABCriterion;
  samplePercent: string;
  windowHours: string;
  variants: VariantDraft[];
}

export interface ResendDraft {
  enabled: boolean;
  subject: string;
  delayHours: string;
}

export type DraftErrors = Record<string, string | undefined>;

/** Letra de la variante i (A, B, C...), la misma que publica el servicio. */
export function variantLabel(i: number): string {
  return String.fromCharCode('A'.charCodeAt(0) + i);
}

const emptyVariant = (): VariantDraft => ({ subject: '', templateId: '', version: '' });

const hours = (minutes: number) => String(Math.round(minutes / MINUTES_PER_HOUR));

export function abDraftFrom(ab: ABTest | null, meta: CampaignsMeta): ABDraft {
  if (!ab) {
    return {
      enabled: false,
      criterion: meta.ab_test.criteria[0] ?? 'opens',
      samplePercent: String(meta.ab_test.min_sample_percent),
      windowHours: hours(meta.ab_test.min_decision_window_minutes),
      variants: Array.from({ length: meta.ab_test.min_variants }, emptyVariant),
    };
  }
  return {
    enabled: true,
    criterion: ab.criterion,
    samplePercent: String(ab.sample_percent),
    windowHours: hours(ab.decision_window_minutes),
    variants: ab.variants.map((v) => ({
      subject: v.subject,
      templateId: v.template_id ?? '',
      version: v.template_version ? String(v.template_version) : '',
    })),
  };
}

export function resendDraftFrom(resend: Resend | null, meta: CampaignsMeta): ResendDraft {
  return resend
    ? { enabled: true, subject: resend.subject, delayHours: hours(resend.delay_minutes) }
    : { enabled: false, subject: '', delayHours: hours(meta.resend.min_delay_minutes) };
}

function integerIn(raw: string, min: number, max: number): number | null {
  const n = Number(raw.trim());
  return raw.trim() !== '' && Number.isInteger(n) && n >= min && n <= max ? n : null;
}

function subjectError(subject: string, max: number, required: boolean): string | undefined {
  const text = subject.trim();
  if (!text) return required ? t('validation.required') : undefined;
  if ([...text].length > max) return t('validation.maxLength', { n: max });
  // Termina en la cabecera Subject: sin saltos de linea ni otros caracteres de control.
  for (const ch of text) {
    const code = ch.codePointAt(0) ?? 0;
    if (code < 0x20 || (code >= 0x7f && code < 0xa0)) return t('campaigns.subject.control');
  }
  return undefined;
}

export interface Built<T> {
  value: T | null;
  errors: DraftErrors;
}

/** Convierte el borrador en el objeto ab_test; null si la prueba esta desactivada. */
export function buildABTest(d: ABDraft, meta: CampaignsMeta): Built<ABTestInput> {
  const errors: DraftErrors = {};
  if (!d.enabled) return { value: null, errors };
  const m = meta.ab_test;
  const sample = integerIn(d.samplePercent, m.min_sample_percent, m.max_sample_percent);
  if (sample === null) {
    errors.samplePercent = t('validation.range', {
      min: m.min_sample_percent,
      max: m.max_sample_percent,
    });
  }
  const minHours = Math.ceil(m.min_decision_window_minutes / MINUTES_PER_HOUR);
  const maxHours = Math.floor(m.max_decision_window_minutes / MINUTES_PER_HOUR);
  const window = integerIn(d.windowHours, minHours, maxHours);
  if (window === null) errors.windowHours = t('validation.range', { min: minHours, max: maxHours });
  if (d.variants.length < m.min_variants || d.variants.length > m.max_variants) {
    errors.variants = t('campaigns.ab.variantCount', { min: m.min_variants, max: m.max_variants });
  }
  const seen = new Set<string>();
  d.variants.forEach((v, i) => {
    const subject = subjectError(v.subject, meta.max_subject_length, false);
    if (subject) errors[`variant.${i}.subject`] = subject;
    if (v.version.trim() && integerIn(v.version, 1, Number.MAX_SAFE_INTEGER) === null) {
      errors[`variant.${i}.version`] = t('campaigns.form.versionInvalid');
    }
    const key = [v.templateId.trim(), v.version.trim(), v.subject.trim()].join('\u0000');
    if (seen.has(key)) errors[`variant.${i}.subject`] = t('campaigns.ab.duplicate');
    seen.add(key);
  });
  if (Object.values(errors).some(Boolean) || sample === null || window === null) {
    return { value: null, errors };
  }
  return {
    value: {
      criterion: d.criterion,
      sample_percent: sample,
      decision_window_minutes: window * MINUTES_PER_HOUR,
      variants: d.variants.map((v) => ({
        subject: v.subject.trim(),
        template_id: v.templateId.trim() || null,
        template_version: v.version.trim() ? Number(v.version) : null,
      })),
    },
    errors,
  };
}

/** Convierte el borrador en el objeto resend; null si el reenvio esta desactivado. */
export function buildResend(d: ResendDraft, meta: CampaignsMeta): Built<Resend> {
  const errors: DraftErrors = {};
  if (!d.enabled) return { value: null, errors };
  const subject = subjectError(d.subject, meta.max_subject_length, true);
  if (subject) errors.resendSubject = subject;
  const minHours = Math.ceil(meta.resend.min_delay_minutes / MINUTES_PER_HOUR);
  const maxHours = Math.floor(meta.resend.max_delay_minutes / MINUTES_PER_HOUR);
  const delay = integerIn(d.delayHours, minHours, maxHours);
  if (delay === null) errors.resendDelay = t('validation.range', { min: minHours, max: maxHours });
  if (subject || delay === null) return { value: null, errors };
  return { value: { subject: d.subject.trim(), delay_minutes: delay * MINUTES_PER_HOUR }, errors };
}

/** Cierto si el objeto construido es la configuracion que ya tiene la campana. */
export function sameABTest(current: ABTest | null, next: ABTestInput | null): boolean {
  if (!current || !next) return current === next;
  const strip = (x: ABTestInput) =>
    JSON.stringify([
      x.criterion,
      x.sample_percent,
      x.decision_window_minutes,
      x.variants.map((v) => [v.subject, v.template_id, v.template_version]),
    ]);
  return strip(current) === strip(next);
}

export function sameResend(current: Resend | null, next: Resend | null): boolean {
  if (!current || !next) return current === next;
  return current.subject === next.subject && current.delay_minutes === next.delay_minutes;
}

// ── Lectura del plan de envio ────────────────────────────────────────────────

export interface VariantRow {
  index: number;
  label: string;
  subject: string;
  templateId: string | null;
  engagement: Engagement | null;
  winner: boolean;
}

/** Filas de la prueba A/B: resultados de la muestra por variante, con la ganadora. */
export function variantRows(c: Campaign, plan: CampaignPlan | null): VariantRow[] {
  if (!c.ab_test) return [];
  return c.ab_test.variants.map((v, index) => ({
    index,
    label: v.label || variantLabel(index),
    subject: v.subject,
    templateId: v.template_id,
    engagement: plan?.engagement.find((e) => e.kind === 'sample' && e.variant === index) ?? null,
    winner: c.ab_winner === index,
  }));
}

export type ABState = 'draft' | 'sampling' | 'deciding' | 'decided';

/** En que punto esta la prueba A/B. */
export function abState(c: Campaign, plan: CampaignPlan | null): ABState {
  if (c.ab_winner !== null) return 'decided';
  const samples = plan?.phases.filter((p) => p.kind === 'sample') ?? [];
  if (!samples.length) return 'draft';
  return samples.every((p) => p.status === 'done') ? 'deciding' : 'sampling';
}

/** Momento en que vence la ventana de decision (not_before de la fase ganadora). */
export function decisionAt(plan: CampaignPlan | null): string | null {
  return plan?.phases.find((p) => p.kind === 'winner')?.not_before ?? null;
}

export type ResendState = 'off' | 'waitingInitial' | 'scheduled' | 'sending' | 'done';

export interface ResendView {
  state: ResendState;
  phase: CampaignPhase | null;
  engagement: Engagement | null;
}

export function resendView(c: Campaign, plan: CampaignPlan | null): ResendView {
  const phase = plan?.phases.find((p) => p.kind === 'resend') ?? null;
  const engagement = plan?.engagement.find((e) => e.kind === 'resend') ?? null;
  if (!c.resend) return { state: 'off', phase, engagement };
  if (!phase) return { state: 'waitingInitial', phase, engagement };
  if (phase.status === 'done') return { state: 'done', phase, engagement };
  return { state: phase.started_at ? 'sending' : 'scheduled', phase, engagement };
}

/** Tramos del envio por zona horaria en orden de salida. */
export function zoneSlots(plan: CampaignPlan | null): CampaignPhase[] {
  return (plan?.phases ?? [])
    .filter((p) => p.kind === 'zone' && p.slot_at)
    .sort((a, b) => (a.slot_at ?? '').localeCompare(b.slot_at ?? ''));
}

/** Zona horaria del navegador, como valor inicial de la zona de respaldo. */
export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  } catch {
    return 'UTC';
  }
}

const LOCAL_DATETIME = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/;

/** Hora de pared del campo datetime-local sin segundos, o null si no es valida. */
export function localSendAt(raw: string): string | null {
  const value = raw.trim().slice(0, 16);
  return LOCAL_DATETIME.test(value) ? value : null;
}
