import { describe, expect, it } from 'vitest';
import { dayRange, eventReason, messageStatusTone, recipientsOf, validRange } from './sendingLog';

describe('pantalla Envios', () => {
  it('el rango de dias incluye el ultimo dia entero', () => {
    const { date_from, date_to } = dayRange('2026-09-20', '2026-09-26');
    expect(new Date(date_from!).getTime()).toBe(new Date('2026-09-20T00:00').getTime());
    expect(new Date(date_to!).getTime()).toBe(new Date('2026-09-27T00:00').getTime());
    expect(dayRange('', '')).toEqual({});
    expect(validRange('2026-09-27', '2026-09-26')).toBe(false);
    expect(validRange('2026-09-26', '')).toBe(true);
  });

  it('el motivo de un rebote es el diagnostico del servidor de destino', () => {
    const base = {
      id: 'e',
      type: 'bounce',
      recipient: 'a@b.pe',
      occurred_at: '2026-09-26T10:00:00Z',
    };
    expect(
      eventReason({
        ...base,
        detail: { reason: 'Permanent/General', diagnostic_code: 'smtp; 550 5.1.1 user unknown' },
      }),
    ).toBe('smtp; 550 5.1.1 user unknown');
    expect(eventReason({ ...base, detail: { reason: 'Permanent/General' } })).toBe(
      'Permanent/General',
    );
    expect(eventReason({ ...base, type: 'delivery', detail: null })).toBe('');
  });

  it('tonos y destinatarios', () => {
    expect(messageStatusTone('delivered')).toBe('success');
    expect(messageStatusTone('bounced')).toBe('danger');
    expect(messageStatusTone('suppressed')).toBe('warning');
    expect(messageStatusTone('queued')).toBe('info');
    expect(recipientsOf([{ email: 'a@b.pe' }, { email: 'c@d.pe' }])).toBe('a@b.pe, c@d.pe');
    expect(recipientsOf(null)).toBe('');
  });
});
