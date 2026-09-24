import { describe, expect, it } from 'vitest';
import type { ExtractedEvent } from '@/api/webmailAssistant';
import { eventProblems, formToInput } from '../calendar/calendar';
import {
  assistantNavigationState,
  assistantTextFromState,
  charCount,
  eventFormFromExtraction,
  eventFormFromTask,
  todayKey,
} from './assistant';

const NOW = new Date(2026, 8, 24, 9, 30);

function event(extra: Partial<ExtractedEvent> = {}): ExtractedEvent {
  return {
    title: 'Reunion con el cliente',
    date: '2026-09-25',
    start_time: '10:00',
    end_time: '11:30',
    location: 'Oficina',
    notes: 'Llevar contrato',
    ...extra,
  };
}

describe('asistente: propuestas al calendario', () => {
  it('una cita con hora se crea en la hora local de quien la confirma', () => {
    const form = eventFormFromExtraction(event(), NOW);
    expect(form).not.toBeNull();
    expect(form).toMatchObject({
      title: 'Reunion con el cliente',
      allDay: false,
      startDate: '2026-09-25',
      startTime: '10:00',
      endDate: '2026-09-25',
      endTime: '11:30',
      location: 'Oficina',
      description: 'Llevar contrato',
      repeat: '',
    });
    expect(eventProblems(form!)).toEqual({});
    const input = formToInput(form!);
    expect(input.start).toBe(new Date(2026, 8, 25, 10, 0).toISOString());
    expect(input.recurrence).toBeNull();
  });

  it('sin hora es de todo el dia y sin fin dura una hora, tambien cruzando la medianoche', () => {
    expect(eventFormFromExtraction(event({ start_time: '', end_time: '' }), NOW)).toMatchObject({
      allDay: true,
      startDate: '2026-09-25',
      endDate: '2026-09-25',
    });
    expect(eventFormFromExtraction(event({ end_time: '' }), NOW)).toMatchObject({
      endTime: '11:00',
      endDate: '2026-09-25',
    });
    expect(
      eventFormFromExtraction(event({ start_time: '23:30', end_time: '' }), NOW),
    ).toMatchObject({ endTime: '00:30', endDate: '2026-09-26' });
  });

  it('una fecha imposible o sin titulo no da formulario', () => {
    expect(eventFormFromExtraction(event({ date: '2026-02-30' }), NOW)).toBeNull();
    expect(eventFormFromExtraction(event({ title: '  ' }), NOW)).toBeNull();
  });

  it('una tarea con vencimiento es un evento de todo el dia; sin fecha, nada', () => {
    expect(
      eventFormFromTask({ title: 'Enviar presupuesto', due_date: '2026-09-26' }, NOW),
    ).toMatchObject({ title: 'Enviar presupuesto', allDay: true, startDate: '2026-09-26' });
    expect(eventFormFromTask({ title: 'Llamar', due_date: '' }, NOW)).toBeNull();
  });
});

describe('asistente: utilidades', () => {
  it('pasa la respuesta propuesta a la redaccion por el estado de navegacion', () => {
    expect(assistantTextFromState(assistantNavigationState('Hola'))).toBe('Hola');
    expect(assistantTextFromState(null)).toBeNull();
    expect(assistantTextFromState({ assistantText: '   ' })).toBeNull();
    expect(assistantTextFromState({ otra: 'cosa' })).toBeNull();
  });

  it('cuenta caracteres como el servicio y da la fecha local de hoy', () => {
    expect(charCount('añ😀')).toBe(3);
    expect(todayKey(NOW)).toBe('2026-09-24');
  });
});
