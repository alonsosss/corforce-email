import { describe, expect, it } from 'vitest';
import { t } from '@/i18n';
import { describeDoiReason, describePause, describeRunError } from './automationReason';

describe('motivos de automations', () => {
  it('la pausa manual y la de una persona con su texto', () => {
    expect(describePause('manual', 'manual')).toEqual({
      text: t('automations.pause.manual'),
      detail: null,
    });
    expect(describePause('revision de textos', 'manual')).toEqual({
      text: t('automations.pause.byPerson'),
      detail: 'revision de textos',
    });
    expect(describePause('  ', 'manual')).toBeNull();
  });

  it('la pausa del ejecutor traduce el codigo y deja el mensaje como detalle', () => {
    expect(describePause('SENDING_RESTRICTED: 20 ejecuciones bloqueadas', 'manual')).toEqual({
      text: t('error.code.SENDING_RESTRICTED'),
      detail: '20 ejecuciones bloqueadas',
    });
  });

  it('el error de una ejecucion por su codigo, propio o de un vecino', () => {
    expect(describeRunError('CONTACT_NOT_SENDABLE', 'baja')).toEqual({
      text: t('automations.runError.CONTACT_NOT_SENDABLE'),
      detail: 'baja',
    });
    expect(describeRunError('TEMPLATE_NOT_MARKETING', '')?.text).toBe(
      t('error.code.TEMPLATE_NOT_MARKETING'),
    );
    expect(describeRunError('CODIGO_NUEVO', '')).toEqual({ text: 'CODIGO_NUEVO', detail: null });
    expect(describeRunError('', '')).toBeNull();
  });

  it('el motivo de un intento del doble opt-in, propio o codificado', () => {
    expect(describeDoiReason('not_configured')?.text).toBe(
      t('automations.doiReason.not_configured'),
    );
    expect(describeDoiReason('SUPPRESSED: unsubscribe')).toEqual({
      text: t('automations.doiReason.SUPPRESSED'),
      detail: 'unsubscribe',
    });
    expect(describeDoiReason('transactional no respondio')).toEqual({
      text: t('automations.reason.other'),
      detail: 'transactional no respondio',
    });
  });
});
