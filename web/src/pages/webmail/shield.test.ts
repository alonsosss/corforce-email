import { describe, expect, it } from 'vitest';
import { t } from '@/i18n';
import { isWarning, reasonText, safeUnsubscribeURL, warningReasons } from './shield';
import { contactFor, meetingsWith, previousMessages, PREVIOUS_SHOWN } from './senderPanel';
import type { Contact, MessageEnvelope, Occurrence } from '@/api/webmail';

describe('escudo antifraude', () => {
  it('explica cada motivo con sus datos y uno desconocido con un texto generico', () => {
    expect(
      reasonText({
        code: 'lookalike_domain',
        level: 'danger',
        params: { domain: 'ernpresa.pe', resembles: 'empresa.pe' },
      }),
    ).toBe(
      t('webmail.shield.reason.lookalike_domain', {
        domain: 'ernpresa.pe',
        resembles: 'empresa.pe',
      }),
    );
    expect(reasonText({ code: 'nuevo_motivo', level: 'caution', params: {} })).toBe(
      t('webmail.shield.reason.unknown'),
    );
  });

  it('la marca de externo no se explica en la banda', () => {
    expect(isWarning('danger')).toBe(true);
    expect(isWarning('caution')).toBe(true);
    expect(isWarning('info')).toBe(false);
    expect(isWarning('none')).toBe(false);
    const reasons = warningReasons([
      { code: 'external_sender', level: 'info', params: {} },
      { code: 'spf_fail', level: 'caution', params: {} },
    ]);
    expect(reasons.map((r) => r.code)).toEqual(['spf_fail']);
  });

  it('solo ofrece paginas de baja https', () => {
    expect(safeUnsubscribeURL('https://tienda.test/baja?t=1')).toBe('https://tienda.test/baja?t=1');
    expect(safeUnsubscribeURL('http://tienda.test/baja')).toBeNull();
    expect(safeUnsubscribeURL('javascript:alert(1)')).toBeNull();
    expect(safeUnsubscribeURL('no es una url')).toBeNull();
    expect(safeUnsubscribeURL(undefined)).toBeNull();
  });
});

function contact(emails: string[]): Contact {
  return {
    id: 'c1',
    etag: '1',
    updated_at: '2026-09-01T00:00:00Z',
    name: 'Carlos',
    given_name: '',
    family_name: '',
    emails: emails.map((value) => ({ value, type: 'work' as const })),
    phones: [],
    organization: '',
    title: '',
    notes: '',
    birthday: '',
  };
}

function occurrence(title: string, start: string, end: string, location = ''): Occurrence {
  return { id: title, title, start, end, location, all_day: false, recurring: false };
}

describe('ficha del remitente', () => {
  it('encuentra el contacto por la direccion sin distinguir mayusculas', () => {
    const list = [contact(['otro@x.test']), contact([' Carlos@Cliente.test '])];
    expect(contactFor(list, 'carlos@cliente.test')).toBe(list[1]);
    expect(contactFor(list, 'nadie@x.test')).toBeNull();
  });

  it('las reuniones que lo mencionan por direccion o nombre completo, en orden y sin las pasadas', () => {
    const now = new Date('2026-09-10T12:00:00Z');
    const list = [
      occurrence('Revision con Jose Perez', '2026-09-15T10:00:00Z', '2026-09-15T11:00:00Z'),
      occurrence('Llamada', '2026-09-11T10:00:00Z', '2026-09-11T11:00:00Z', 'jose@cliente.test'),
      occurrence('Pasada con Jose Perez', '2026-09-01T10:00:00Z', '2026-09-01T11:00:00Z'),
      occurrence('Otra cosa', '2026-09-12T10:00:00Z', '2026-09-12T11:00:00Z'),
      occurrence('Con Jo', '2026-09-12T10:00:00Z', '2026-09-12T11:00:00Z'),
    ];
    const found = meetingsWith(list, { name: 'José  Pérez', email: 'JOSE@cliente.test' }, now);
    expect(found.map((o) => o.title)).toEqual(['Llamada', 'Revision con Jose Perez']);
    expect(meetingsWith(list, { name: 'Jo', email: '' }, now)).toEqual([]);
  });

  it('los correos anteriores no incluyen el que se esta leyendo y se acotan', () => {
    const items = Array.from(
      { length: PREVIOUS_SHOWN + 3 },
      (_, i) => ({ uid: i + 1 }) as MessageEnvelope,
    );
    const shown = previousMessages(items, 'INBOX', { folder: 'INBOX', uid: 1 });
    expect(shown).toHaveLength(PREVIOUS_SHOWN);
    expect(shown[0]?.uid).toBe(2);
    expect(previousMessages(items, 'INBOX', { folder: 'Archivo', uid: 1 })[0]?.uid).toBe(1);
  });
});
