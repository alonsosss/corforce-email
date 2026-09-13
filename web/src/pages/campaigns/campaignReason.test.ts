import { describe, expect, it } from 'vitest';
import { t } from '@/i18n';
import { campaignStatusInfo, type CampaignsMeta } from '@/api/campaigns';
import { describeReason } from './campaignReason';

const MANUAL = 'manual';

describe('motivo de pausa o de fallo de una campana', () => {
  it('la pausa manual se dice con su texto', () => {
    expect(describeReason(MANUAL, MANUAL)).toEqual({
      text: t('campaigns.reason.manual'),
      detail: null,
    });
  });

  it('un codigo conocido se traduce y el mensaje del servicio queda como detalle', () => {
    expect(describeReason('SENDING_RESTRICTED: reputacion restringida', MANUAL)).toEqual({
      text: t('error.code.SENDING_RESTRICTED'),
      detail: 'reputacion restringida',
    });
    expect(describeReason('TEMPLATE_MISSING_UNSUBSCRIBE: falta el enlace', MANUAL)?.text).toBe(
      t('error.code.TEMPLATE_MISSING_UNSUBSCRIBE'),
    );
  });

  it('un codigo desconocido se muestra tal cual, sin inventar un texto', () => {
    expect(describeReason('NUEVO_CODIGO: algo', MANUAL)).toEqual({
      text: 'NUEVO_CODIGO',
      detail: 'algo',
    });
  });

  it('un motivo libre se marca como motivo del servicio', () => {
    expect(describeReason('lote 3 sin entregar tras 10 intentos: timeout', MANUAL)).toEqual({
      text: t('campaigns.reason.other'),
      detail: 'lote 3 sin entregar tras 10 intentos: timeout',
    });
    expect(describeReason('  ', MANUAL)).toBeNull();
  });

  it('sin catalogo, el motivo manual no se reconoce y se muestra tal cual', () => {
    expect(describeReason(MANUAL, null)).toEqual({
      text: t('campaigns.reason.other'),
      detail: MANUAL,
    });
  });
});

describe('acciones que se ofrecen por estado', () => {
  const info = (status: 'draft' | 'paused') => ({
    status,
    editable: true,
    content_locked: status === 'paused',
    can_schedule: status === 'draft',
    can_start: status === 'draft',
    can_pause: false,
    can_resume: status === 'paused',
    can_cancel: status === 'paused',
    deletable: status === 'draft',
  });
  const meta = { statuses: [info('draft'), info('paused')] } as unknown as CampaignsMeta;

  it('las toma del catalogo del servicio, sin reglas propias', () => {
    expect(campaignStatusInfo(meta, 'paused')?.content_locked).toBe(true);
    expect(campaignStatusInfo(meta, 'draft')?.can_start).toBe(true);
  });

  it('un estado que el catalogo no trae no ofrece acciones', () => {
    expect(campaignStatusInfo(meta, 'sending')).toBeNull();
  });
});
