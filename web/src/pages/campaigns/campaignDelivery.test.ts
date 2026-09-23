import { describe, expect, it } from 'vitest';
import { t } from '@/i18n';
import type { Campaign, CampaignPlan, CampaignsMeta } from '@/api/campaigns';
import {
  abDraftFrom,
  abState,
  buildABTest,
  buildResend,
  decisionAt,
  localSendAt,
  resendDraftFrom,
  resendView,
  sameABTest,
  sameResend,
  variantLabel,
  variantRows,
  zoneSlots,
  type ABDraft,
} from './campaignDelivery';

const meta = {
  ab_test: {
    criteria: ['opens', 'clicks'],
    min_variants: 2,
    max_variants: 4,
    min_sample_percent: 10,
    max_sample_percent: 50,
    min_decision_window_minutes: 60,
    max_decision_window_minutes: 4320,
    decision_reasons: ['criterion', 'secondary', 'delivered', 'first'],
  },
  resend: { min_delay_minutes: 1440, max_delay_minutes: 10080 },
  phases: { kinds: ['main', 'sample', 'winner', 'zone', 'resend'], statuses: ['pending', 'done'] },
  max_subject_length: 20,
} as unknown as CampaignsMeta;

function draft(patch: Partial<ABDraft> = {}): ABDraft {
  return {
    enabled: true,
    criterion: 'opens',
    samplePercent: '20',
    windowHours: '4',
    variants: [
      { subject: 'Oferta', templateId: '', version: '' },
      { subject: 'Solo hoy', templateId: 'tpl-2', version: '3' },
    ],
    ...patch,
  };
}

const campaign = (patch: Partial<Campaign> = {}): Campaign =>
  ({
    id: 'c1',
    ab_test: null,
    ab_winner: null,
    ab_decided_at: null,
    resend: null,
    timezone_delivery: null,
    ...patch,
  }) as Campaign;

describe('borrador de la prueba A/B', () => {
  it('sin prueba parte de los minimos de la meta', () => {
    const d = abDraftFrom(null, meta);
    expect(d).toEqual({
      enabled: false,
      criterion: 'opens',
      samplePercent: '10',
      windowHours: '1',
      variants: [
        { subject: '', templateId: '', version: '' },
        { subject: '', templateId: '', version: '' },
      ],
    });
    expect(buildABTest(d, meta)).toEqual({ value: null, errors: {} });
  });

  it('convierte horas en minutos y vacios en null', () => {
    expect(
      buildABTest(
        draft({
          variants: [draft().variants[0]!, { subject: ' B ', templateId: '', version: '' }],
        }),
        meta,
      ).value,
    ).toEqual({
      criterion: 'opens',
      sample_percent: 20,
      decision_window_minutes: 240,
      variants: [
        { subject: 'Oferta', template_id: null, template_version: null },
        { subject: 'B', template_id: null, template_version: null },
      ],
    });
    const built = buildABTest(draft(), meta).value;
    expect(built?.variants[1]).toEqual({
      subject: 'Solo hoy',
      template_id: 'tpl-2',
      template_version: 3,
    });
  });

  it('valida los topes de la meta', () => {
    const errors = buildABTest(
      draft({
        samplePercent: '60',
        windowHours: '0',
        variants: [{ subject: 'x'.repeat(21), templateId: '', version: '0' }],
      }),
      meta,
    ).errors;
    expect(errors.samplePercent).toBe(t('validation.range', { min: 10, max: 50 }));
    expect(errors.windowHours).toBe(t('validation.range', { min: 1, max: 72 }));
    expect(errors.variants).toBe(t('campaigns.ab.variantCount', { min: 2, max: 4 }));
    expect(errors['variant.0.subject']).toBe(t('validation.maxLength', { n: 20 }));
    expect(errors['variant.0.version']).toBe(t('campaigns.form.versionInvalid'));
  });

  it('rechaza asuntos con saltos de linea y variantes repetidas', () => {
    const errors = buildABTest(
      draft({
        variants: [
          { subject: 'Hola\nBcc: x', templateId: '', version: '' },
          { subject: 'Igual', templateId: '', version: '' },
          { subject: 'Igual ', templateId: '', version: '' },
        ],
      }),
      meta,
    ).errors;
    expect(errors['variant.0.subject']).toBe(t('campaigns.subject.control'));
    expect(errors['variant.2.subject']).toBe(t('campaigns.ab.duplicate'));
    expect(errors['variant.1.subject']).toBeUndefined();
  });

  it('vuelve al borrador desde la campana y detecta si cambio', () => {
    const ab = {
      criterion: 'clicks' as const,
      sample_percent: 30,
      decision_window_minutes: 120,
      variants: [
        {
          label: 'A',
          subject: 'Oferta',
          template_id: null,
          template_version: null,
          pinned_version: 4,
        },
        {
          label: 'B',
          subject: 'Solo hoy',
          template_id: null,
          template_version: null,
          pinned_version: 4,
        },
      ],
    };
    const d = abDraftFrom(ab, meta);
    expect(d.windowHours).toBe('2');
    const rebuilt = buildABTest(d, meta).value;
    expect(sameABTest(ab, rebuilt)).toBe(true);
    expect(sameABTest(ab, { ...rebuilt!, sample_percent: 31 })).toBe(false);
    expect(sameABTest(null, null)).toBe(true);
    expect(sameABTest(ab, null)).toBe(false);
  });
});

describe('borrador del reenvio', () => {
  it('exige asunto y un retraso dentro de los topes', () => {
    const off = resendDraftFrom(null, meta);
    expect(off).toEqual({ enabled: false, subject: '', delayHours: '24' });
    const errors = buildResend({ enabled: true, subject: ' ', delayHours: '200' }, meta).errors;
    expect(errors.resendSubject).toBe(t('validation.required'));
    expect(errors.resendDelay).toBe(t('validation.range', { min: 24, max: 168 }));
    const ok = buildResend({ enabled: true, subject: ' Otra vez ', delayHours: '48' }, meta);
    expect(ok.value).toEqual({ subject: 'Otra vez', delay_minutes: 2880 });
    expect(sameResend({ subject: 'Otra vez', delay_minutes: 2880 }, ok.value)).toBe(true);
    expect(sameResend(null, ok.value)).toBe(false);
    expect(resendDraftFrom(ok.value, meta)).toEqual({
      enabled: true,
      subject: 'Otra vez',
      delayHours: '48',
    });
  });
});

const plan: CampaignPlan = {
  phases: [
    {
      id: 's0',
      kind: 'sample',
      variant: 0,
      slot_at: null,
      not_before: null,
      status: 'done',
      targeted: 10,
      accepted: 10,
      suppressed: 0,
      started_at: 'x',
      completed_at: 'y',
    },
    {
      id: 's1',
      kind: 'sample',
      variant: 1,
      slot_at: null,
      not_before: null,
      status: 'done',
      targeted: 10,
      accepted: 9,
      suppressed: 1,
      started_at: 'x',
      completed_at: 'y',
    },
    {
      id: 'w',
      kind: 'winner',
      variant: null,
      slot_at: null,
      not_before: '2026-10-01T12:00:00Z',
      status: 'pending',
      targeted: 0,
      accepted: 0,
      suppressed: 0,
      started_at: null,
      completed_at: null,
    },
    {
      id: 'z2',
      kind: 'zone',
      variant: null,
      slot_at: '2026-10-01T14:00:00Z',
      not_before: null,
      status: 'pending',
      targeted: 0,
      accepted: 0,
      suppressed: 0,
      started_at: null,
      completed_at: null,
    },
    {
      id: 'z1',
      kind: 'zone',
      variant: null,
      slot_at: '2026-10-01T07:00:00Z',
      not_before: null,
      status: 'done',
      targeted: 3,
      accepted: 3,
      suppressed: 0,
      started_at: 'x',
      completed_at: 'y',
    },
  ],
  engagement: [
    {
      kind: 'sample',
      variant: 1,
      accepted: 9,
      delivered: 9,
      opened: 3,
      clicked: 1,
      open_rate: '0.3333',
      click_rate: '0.1111',
    },
    {
      kind: 'resend',
      variant: null,
      accepted: 4,
      delivered: 4,
      opened: 1,
      clicked: 0,
      open_rate: '0.2500',
      click_rate: '0.0000',
    },
  ],
};

describe('lectura del plan de envio', () => {
  const ab = {
    criterion: 'opens' as const,
    sample_percent: 20,
    decision_window_minutes: 60,
    variants: [
      {
        label: 'A',
        subject: 'Oferta',
        template_id: null,
        template_version: null,
        pinned_version: 1,
      },
      { label: 'B', subject: '', template_id: null, template_version: null, pinned_version: 1 },
    ],
  };

  it('filas por variante con sus resultados y la ganadora', () => {
    const rows = variantRows(campaign({ ab_test: ab, ab_winner: 1 }), plan);
    expect(rows.map((r) => [r.label, r.winner, r.engagement?.opened ?? null])).toEqual([
      ['A', false, null],
      ['B', true, 3],
    ]);
    expect(variantRows(campaign(), plan)).toEqual([]);
  });

  it('estado de la prueba y momento de la decision', () => {
    expect(abState(campaign({ ab_test: ab }), { phases: [], engagement: [] })).toBe('draft');
    expect(abState(campaign({ ab_test: ab }), plan)).toBe('deciding');
    const sampling = {
      ...plan,
      phases: plan.phases.map((p) => (p.id === 's1' ? { ...p, status: 'pending' as const } : p)),
    };
    expect(abState(campaign({ ab_test: ab }), sampling)).toBe('sampling');
    expect(abState(campaign({ ab_test: ab, ab_winner: 0 }), plan)).toBe('decided');
    expect(decisionAt(plan)).toBe('2026-10-01T12:00:00Z');
    expect(decisionAt(null)).toBeNull();
  });

  it('estado del reenvio', () => {
    const resend = { subject: 'Otra vez', delay_minutes: 1440 };
    expect(resendView(campaign(), plan).state).toBe('off');
    expect(resendView(campaign({ resend }), plan).state).toBe('waitingInitial');
    const phase = {
      id: 'r',
      kind: 'resend' as const,
      variant: null,
      slot_at: null,
      not_before: '2026-10-03T00:00:00Z',
      status: 'pending' as const,
      targeted: 0,
      accepted: 0,
      suppressed: 0,
      started_at: null,
      completed_at: null,
    };
    const withResend = { ...plan, phases: [...plan.phases, phase] };
    expect(resendView(campaign({ resend }), withResend).state).toBe('scheduled');
    expect(
      resendView(campaign({ resend }), {
        ...withResend,
        phases: [...plan.phases, { ...phase, started_at: 'x' }],
      }).state,
    ).toBe('sending');
    const done = resendView(campaign({ resend }), {
      ...withResend,
      phases: [...plan.phases, { ...phase, status: 'done', started_at: 'x', completed_at: 'y' }],
    });
    expect(done.state).toBe('done');
    expect(done.engagement?.opened).toBe(1);
  });

  it('tramos de zona en orden de salida', () => {
    expect(zoneSlots(plan).map((p) => p.id)).toEqual(['z1', 'z2']);
    expect(zoneSlots(null)).toEqual([]);
  });
});

describe('utilidades', () => {
  it('letras de variante y hora local', () => {
    expect([0, 1, 2, 3].map(variantLabel)).toEqual(['A', 'B', 'C', 'D']);
    expect(localSendAt('2026-10-01T09:30')).toBe('2026-10-01T09:30');
    expect(localSendAt('2026-10-01T09:30:15')).toBe('2026-10-01T09:30');
    expect(localSendAt('')).toBeNull();
    expect(localSendAt('manana a las 9')).toBeNull();
  });
});
