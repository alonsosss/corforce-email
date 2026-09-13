import { describe, expect, it } from 'vitest';
import { t } from '@/i18n';
import { describeReason } from './campaignReason';
import { campaignRules } from './campaignRules';

describe('motivo de pausa o de fallo de una campana', () => {
  it('la pausa manual se dice con su texto', () => {
    expect(describeReason('manual')).toEqual({ text: t('campaigns.reason.manual'), detail: null });
  });

  it('un codigo conocido se traduce y el mensaje del servicio queda como detalle', () => {
    expect(describeReason('SENDING_RESTRICTED: reputacion restringida')).toEqual({
      text: t('error.code.SENDING_RESTRICTED'),
      detail: 'reputacion restringida',
    });
    expect(describeReason('TEMPLATE_MISSING_UNSUBSCRIBE: falta el enlace')?.text).toBe(
      t('error.code.TEMPLATE_MISSING_UNSUBSCRIBE'),
    );
  });

  it('un codigo desconocido se muestra tal cual, sin inventar un texto', () => {
    expect(describeReason('NUEVO_CODIGO: algo')).toEqual({ text: 'NUEVO_CODIGO', detail: 'algo' });
  });

  it('un motivo libre se marca como motivo del servicio', () => {
    expect(describeReason('lote 3 sin entregar tras 10 intentos: timeout')).toEqual({
      text: t('campaigns.reason.other'),
      detail: 'lote 3 sin entregar tras 10 intentos: timeout',
    });
    expect(describeReason('  ')).toBeNull();
  });
});

describe('acciones que se ofrecen por estado', () => {
  it('sigue las transiciones del dominio de campaigns', () => {
    expect(campaignRules.schedulable('draft')).toBe(true);
    expect(campaignRules.schedulable('sending')).toBe(false);
    expect(campaignRules.pausable('scheduled')).toBe(true);
    expect(campaignRules.resumable('paused')).toBe(true);
    expect(campaignRules.cancellable('completed')).toBe(false);
    expect(campaignRules.deletable('cancelled')).toBe(true);
    expect(campaignRules.deletable('sending')).toBe(false);
    expect(campaignRules.editable('paused') && campaignRules.contentLocked('paused')).toBe(true);
  });
});
