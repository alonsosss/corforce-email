import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ApiError } from '@/api/errors';
import type { Vacation } from '@/api/vacation';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import { toDraft, toInput, validateVacation, VacationForm } from './VacationForm';

const LIMITS = {
  subject_max_length: 20,
  message_max_length: 30,
  interval_min_days: 1,
  interval_max_days: 30,
};

const BASE: Vacation = {
  enabled: false,
  subject: '',
  message: '',
  interval_days: 1,
  starts_on: null,
  ends_on: null,
  updated_at: null,
  limits: LIMITS,
};

function draftOf(patch: Partial<Vacation>) {
  return toDraft({ ...BASE, ...patch });
}

describe('validacion de la respuesta automatica', () => {
  it('una respuesta activa necesita mensaje; desactivada puede guardarse en blanco', () => {
    expect(validateVacation(draftOf({ enabled: true }), LIMITS).message).toBe(
      t('vacation.error.messageRequired'),
    );
    expect(validateVacation(draftOf({ enabled: true, message: '   \n ' }), LIMITS).message).toBe(
      t('vacation.error.messageRequired'),
    );
    expect(validateVacation(draftOf({ enabled: false }), LIMITS)).toEqual({});
    expect(
      validateVacation(draftOf({ enabled: true, message: 'Vuelvo el lunes.' }), LIMITS),
    ).toEqual({});
  });

  it('los topes son los que manda el servicio y se cuentan en caracteres, no en bytes', () => {
    const justo = 'ñ'.repeat(30);
    expect(validateVacation(draftOf({ enabled: true, message: justo }), LIMITS)).toEqual({});
    expect(validateVacation(draftOf({ enabled: true, message: justo + 'a' }), LIMITS).message).toBe(
      t('vacation.error.messageTooLong', { max: 30 }),
    );
    expect(
      validateVacation(draftOf({ enabled: true, message: 'x', subject: 'a'.repeat(21) }), LIMITS)
        .subject,
    ).toBe(t('vacation.error.subjectTooLong', { max: 20 }));
    // Con otros topes del servicio, el mismo texto pasa: no hay un numero fijo en la interfaz.
    const holgados = { ...LIMITS, message_max_length: 100 };
    expect(validateVacation(draftOf({ enabled: true, message: justo + 'a' }), holgados)).toEqual(
      {},
    );
  });

  it('el asunto no admite saltos de linea', () => {
    expect(
      validateVacation(draftOf({ enabled: true, message: 'x', subject: 'a\nBcc: x@y.z' }), LIMITS)
        .subject,
    ).toBe(t('vacation.error.subjectLine'));
  });

  it('el intervalo es un entero dentro del rango del servicio', () => {
    const error = t('vacation.error.interval', { min: 1, max: 30 });
    for (const bad of ['0', '31', '1.5', '', 'abc', '-2']) {
      const d = { ...draftOf({}), interval: bad };
      expect(validateVacation(d, LIMITS).interval, bad).toBe(error);
    }
    for (const good of ['1', '7', '30']) {
      expect(
        validateVacation({ ...draftOf({}), interval: good }, LIMITS).interval,
        good,
      ).toBeUndefined();
    }
  });

  it('las fechas son AAAA-MM-DD y el fin no puede ser anterior al inicio', () => {
    expect(
      validateVacation(draftOf({ starts_on: '2026-09-30', ends_on: '2026-09-21' }), LIMITS).endsOn,
    ).toBe(t('vacation.error.window'));
    expect(
      validateVacation(draftOf({ starts_on: '2026-09-21', ends_on: '2026-09-21' }), LIMITS),
    ).toEqual({});
    expect(validateVacation(draftOf({ starts_on: '2026-13-40' }), LIMITS).startsOn).toBe(
      t('vacation.error.date'),
    );
    expect(validateVacation(draftOf({ ends_on: 'manana' }), LIMITS).endsOn).toBe(
      t('vacation.error.date'),
    );
  });

  it('lo que se envia es lo que el servicio espera: numero, fechas o null, asunto sin espacios sobrantes', () => {
    const d = {
      ...draftOf({ enabled: true, message: 'Hola' }),
      subject: '  Ausente  ',
      interval: '3',
      startsOn: '2026-09-21',
      endsOn: '',
    };
    expect(toInput(d)).toEqual({
      enabled: true,
      subject: 'Ausente',
      message: 'Hola',
      interval_days: 3,
      starts_on: '2026-09-21',
      ends_on: null,
    });
  });
});

function renderForm(props: Partial<Parameters<typeof VacationForm>[0]> = {}) {
  const onSave = vi.fn(async (input) => ({
    ...BASE,
    ...input,
    updated_at: '2026-09-21T10:00:00Z',
  }));
  render(
    <ToastProvider>
      <VacationForm initial={BASE} editable onSave={onSave} {...props} />
    </ToastProvider>,
  );
  return { onSave };
}

describe('formulario de la respuesta automatica', () => {
  it('no envia nada si hay un error y lo muestra junto al campo', async () => {
    const user = userEvent.setup();
    const { onSave } = renderForm();
    await user.click(screen.getByLabelText(t('vacation.enabled')));
    await user.click(screen.getByRole('button', { name: t('common.save') }));

    expect(await screen.findByText(t('vacation.error.messageRequired'))).toBeInTheDocument();
    expect(onSave).not.toHaveBeenCalled();
  });

  it('guarda lo escrito y se reinicia con lo que devuelve el servicio', async () => {
    const user = userEvent.setup();
    const { onSave } = renderForm();
    await user.click(screen.getByLabelText(t('vacation.enabled')));
    await user.type(screen.getByLabelText(new RegExp(`^${t('vacation.subject')}`)), 'Ausente');
    await user.type(screen.getByLabelText(new RegExp(`^${t('vacation.message')}`)), 'Vuelvo.');
    await user.click(screen.getByRole('button', { name: t('common.save') }));

    expect(onSave).toHaveBeenCalledWith({
      enabled: true,
      subject: 'Ausente',
      message: 'Vuelvo.',
      interval_days: 1,
      starts_on: null,
      ends_on: null,
    });
    expect(await screen.findByText(t('vacation.saved'))).toBeInTheDocument();
  });

  it('muestra el motivo que rechaza el servicio sin perder lo escrito', async () => {
    const user = userEvent.setup();
    const onSave = vi
      .fn()
      .mockRejectedValue(
        new ApiError(422, { code: 'VALIDATION_ERROR', message: 'el mensaje supera los 8192' }),
      );
    renderForm({ onSave });
    await user.click(screen.getByLabelText(t('vacation.enabled')));
    await user.type(screen.getByLabelText(new RegExp(`^${t('vacation.message')}`)), 'Hola');
    await user.click(screen.getByRole('button', { name: t('common.save') }));

    expect(await screen.findByRole('alert')).toHaveTextContent('el mensaje supera los 8192');
    expect(screen.getByLabelText(new RegExp(`^${t('vacation.message')}`))).toHaveValue('Hola');
  });

  it('sin permiso para editar no hay boton y los campos no se pueden cambiar', () => {
    renderForm({ editable: false });
    expect(screen.queryByRole('button', { name: t('common.save') })).not.toBeInTheDocument();
    expect(screen.getByLabelText(new RegExp(`^${t('vacation.message')}`))).toHaveAttribute(
      'readonly',
    );
    expect(screen.getByLabelText(t('vacation.enabled'))).toBeDisabled();
  });
});
