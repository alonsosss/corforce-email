import { describe, expect, it } from 'vitest';
import type { CalendarEvent } from '@/api/webmail';
import { t } from '@/i18n';
import {
  dayKey,
  eventProblems,
  eventToForm,
  formToInput,
  groupByDay,
  mergeOccurrences,
  instantInZone,
  newEventForm,
  occurrenceDays,
  occurrenceToForm,
  overlapping,
  splitWindow,
  timeZoneOptions,
  visibleRange,
  wallInZone,
} from './calendar';

describe('ventana visible del calendario', () => {
  it('el mes son seis semanas de lunes a domingo y la semana empieza en lunes', () => {
    const month = visibleRange('month', new Date(2026, 8, 24));
    expect(month.days).toHaveLength(42);
    expect(month.start.getDay()).toBe(1);
    expect(dayKey(month.start)).toBe('2026-08-31');
    const week = visibleRange('week', new Date(2026, 8, 24));
    expect(dayKey(week.start)).toBe('2026-09-21');
    expect(dayKey(week.end)).toBe('2026-09-28');
    expect(visibleRange('agenda', new Date(2026, 1, 10)).days).toHaveLength(28);
  });

  it('parte la peticion si la ventana del servicio es menor y no repite ocurrencias', () => {
    const start = new Date(2026, 8, 1);
    const end = new Date(2026, 9, 13);
    expect(splitWindow(start, end, null)).toHaveLength(1);
    const parts = splitWindow(start, end, 31);
    expect(parts).toHaveLength(2);
    expect(parts[1]?.end).toEqual(end);
    const o = {
      id: 'a',
      start: '2026-09-01T10:00:00Z',
      end: '',
      all_day: false,
      title: '',
      location: '',
      recurring: false,
      recurrence_id: '2026-09-01T10:00:00Z',
    };
    expect(mergeOccurrences([[o], [o]])).toHaveLength(1);
  });
});

describe('dias que ocupa una ocurrencia', () => {
  it('todo el dia: medianoche UTC con fin exclusivo es un solo dia', () => {
    expect(
      occurrenceDays({ start: '2026-10-01T00:00:00Z', end: '2026-10-02T00:00:00Z', all_day: true }),
    ).toEqual(['2026-10-01']);
    expect(
      occurrenceDays({ start: '2026-10-01T00:00:00Z', end: '2026-10-04T00:00:00Z', all_day: true }),
    ).toEqual(['2026-10-01', '2026-10-02', '2026-10-03']);
  });

  it('agrupa por dia con los de todo el dia primero', () => {
    const byDay = groupByDay([
      {
        id: 'b',
        start: '2026-10-01T10:00:00Z',
        end: '2026-10-01T11:00:00Z',
        all_day: false,
        title: 'B',
        location: '',
        recurring: false,
        recurrence_id: '2026-10-01T10:00:00Z',
      },
      {
        id: 'a',
        start: '2026-10-01T00:00:00Z',
        end: '2026-10-02T00:00:00Z',
        all_day: true,
        title: 'A',
        location: '',
        recurring: false,
        recurrence_id: '2026-10-01T00:00:00Z',
      },
    ]);
    const key = dayKey(new Date('2026-10-01T10:00:00Z'));
    expect(byDay.get(key)?.[0]?.id).toBe('a');
  });
});

describe('formulario del evento', () => {
  it('todo el dia se envia a medianoche UTC con el fin exclusivo', () => {
    const form = {
      ...newEventForm(new Date(2026, 9, 1)),
      title: ' Congreso ',
      allDay: true,
      startDate: '2026-10-01',
      endDate: '2026-10-03',
    };
    expect(eventProblems(form)).toEqual({});
    const input = formToInput(form);
    expect(input).toMatchObject({
      title: 'Congreso',
      start: '2026-10-01T00:00:00Z',
      end: '2026-10-04T00:00:00Z',
      all_day: true,
      recurrence: null,
    });
    const back = eventToForm({ ...input, id: 'e1', etag: 'x' } as CalendarEvent);
    expect(back.endDate).toBe('2026-10-03');
  });

  it('la repeticion semanal lleva sus dias y un fin por numero de veces', () => {
    const form = {
      ...newEventForm(new Date(2026, 9, 1)),
      title: 'Reunion',
      repeat: 'weekly' as const,
      interval: '2',
      ends: 'count' as const,
      count: '5',
      byDay: ['MO' as const, 'WE' as const],
      reminder: '15',
    };
    expect(formToInput(form)).toMatchObject({
      recurrence: { freq: 'weekly', interval: 2, count: 5, until: null, by_day: ['MO', 'WE'] },
      reminder_minutes: 15,
    });
  });

  it('senala titulo vacio, fin anterior al inicio y repeticion invalida', () => {
    const problems = eventProblems({
      ...newEventForm(new Date(2026, 9, 1)),
      title: '',
      startDate: '2026-10-02',
      startTime: '10:00',
      endDate: '2026-10-02',
      endTime: '09:00',
      repeat: 'daily',
      interval: '0',
      ends: 'until',
      until: '2026-09-01',
    });
    expect(problems).toEqual({
      title: t('webmail.calendar.titleRequired'),
      end: t('webmail.calendar.endBeforeStart'),
      interval: t('webmail.calendar.intervalInvalid'),
      until: t('webmail.calendar.untilInvalid'),
    });
  });
});

describe('zona horaria del evento', () => {
  it('convierte la hora de pared de una zona a un instante a ambos lados del cambio de hora', () => {
    // Madrid: +02:00 en verano, +01:00 desde el 25 de octubre de 2026.
    expect(instantInZone('2026-10-19', '10:00', 'Europe/Madrid')?.toISOString()).toBe(
      '2026-10-19T08:00:00.000Z',
    );
    expect(instantInZone('2026-10-26', '10:00', 'Europe/Madrid')?.toISOString()).toBe(
      '2026-10-26T09:00:00.000Z',
    );
    expect(instantInZone('2026-10-01', '09:30', 'America/Lima')?.toISOString()).toBe(
      '2026-10-01T14:30:00.000Z',
    );
    expect(wallInZone(new Date('2026-10-26T09:00:00Z'), 'Europe/Madrid')).toEqual({
      date: '2026-10-26',
      time: '10:00',
    });
    expect(instantInZone('2026-10-01', '25:00', 'UTC')).toBeNull();
  });

  it('el formulario se lee y se escribe en la zona del evento, no en la del navegador', () => {
    const event: CalendarEvent = {
      id: 'e',
      etag: '"1"',
      title: 'Comite',
      start: '2026-10-19T08:00:00Z',
      end: '2026-10-19T09:00:00Z',
      all_day: false,
      timezone: 'Europe/Madrid',
      location: '',
      description: '',
      recurrence: null,
      reminder_minutes: null,
      attendees: [{ email: 'bea@empresa.pe', name: 'Bea', partstat: 'ACCEPTED' }],
      organizer: { email: 'ana@empresa.pe', name: 'Ana' },
    };
    const form = eventToForm(event);
    expect(form).toMatchObject({
      startDate: '2026-10-19',
      startTime: '10:00',
      timezone: 'Europe/Madrid',
    });
    const input = formToInput({ ...form, attendees: ['bea@empresa.pe', 'carlos@empresa.pe'] });
    expect(input.start).toBe('2026-10-19T08:00:00.000Z');
    expect(input.timezone).toBe('Europe/Madrid');
    // El invitado que ya respondio conserva su respuesta; el nuevo queda pendiente.
    expect(input.attendees).toEqual([
      { email: 'bea@empresa.pe', name: 'Bea', partstat: 'ACCEPTED' },
      { email: 'carlos@empresa.pe', name: '', partstat: 'NEEDS-ACTION' },
    ]);
    expect(formToInput({ ...form, allDay: true }).timezone).toBe('');

    const one = occurrenceToForm(
      {
        ...event,
        recurrence: { freq: 'weekly', interval: 1, count: null, until: null, by_day: null },
      },
      {
        start: '2026-10-26T09:00:00Z',
        end: '2026-10-26T10:00:00Z',
        title: 'Comite (movido)',
        location: 'Sala 3',
      },
    );
    expect(one).toMatchObject({
      startDate: '2026-10-26',
      startTime: '10:00',
      title: 'Comite (movido)',
      location: 'Sala 3',
      repeat: '',
    });
  });

  it('ofrece la zona actual aunque el navegador no la liste y detecta choques de ocupacion', () => {
    expect(timeZoneOptions('Etc/GMT+5')).toContain('Etc/GMT+5');
    expect(timeZoneOptions('')).toContain('UTC');
    const busy = [{ start: '2026-10-01T10:00:00Z', end: '2026-10-01T11:00:00Z' }];
    expect(
      overlapping(busy, new Date('2026-10-01T10:30:00Z'), new Date('2026-10-01T12:00:00Z')),
    ).toHaveLength(1);
    expect(
      overlapping(busy, new Date('2026-10-01T11:00:00Z'), new Date('2026-10-01T12:00:00Z')),
    ).toHaveLength(0);
  });
});
