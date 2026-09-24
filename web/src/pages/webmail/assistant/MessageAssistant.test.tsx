import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { ApiError } from '@/api/errors';
import { webmailApi, type CalendarEvent } from '@/api/webmail';
import { assistantApi, type AssistantStatus } from '@/api/webmailAssistant';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import { MessageAssistant } from './MessageAssistant';

const STATUS: AssistantStatus = {
  available: true,
  actions: ['summarize', 'reply', 'tone', 'extract'],
  tones: ['formal', 'friendly', 'brief'],
  limits: {
    max_input_chars: 100,
    max_thread_messages: 5,
    max_instruction_chars: 10,
    mailbox_daily: 50,
    tenant_daily: 500,
  },
  usage: { mailbox: 3, tenant: 7 },
};

function renderAssistant(onReplyWith = vi.fn()) {
  render(
    <MemoryRouter>
      <ToastProvider>
        <MessageAssistant folder="INBOX" uid={5} onReplyWith={onReplyWith} />
      </ToastProvider>
    </MemoryRouter>,
  );
  return onReplyWith;
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('asistente en el lector', () => {
  it('no se ofrece si la empresa no lo activo', async () => {
    const status = vi
      .spyOn(assistantApi, 'status')
      .mockResolvedValue({ ...STATUS, available: false, reason: 'disabled' });
    renderAssistant();
    await waitFor(() => expect(status).toHaveBeenCalled());
    expect(screen.queryByText(t('webmail.assistant.summarize'))).not.toBeInTheDocument();
  });

  it('resume el mensaje y muestra el resultado como texto con el aviso', async () => {
    vi.spyOn(assistantApi, 'status').mockResolvedValue(STATUS);
    const summarize = vi.spyOn(assistantApi, 'summarize').mockResolvedValue({
      text: '<b>Resumen</b>',
      input_truncated: true,
      output_truncated: false,
    });
    renderAssistant();
    await userEvent.click(
      await screen.findByRole('button', { name: t('webmail.assistant.summarize') }),
    );
    expect(summarize).toHaveBeenCalledWith([{ folder: 'INBOX', uid: 5 }]);
    expect(await screen.findByText('<b>Resumen</b>')).toBeInTheDocument();
    expect(screen.getByText(t('webmail.assistant.privacy'))).toBeInTheDocument();
    expect(screen.getByText(t('webmail.assistant.inputTruncated'))).toBeInTheDocument();
    expect(
      screen.getByText(t('webmail.assistant.usage', { used: 3, limit: 50 })),
    ).toBeInTheDocument();
  });

  it('propone una respuesta con indicaciones acotadas y la pasa a la redaccion solo si se pide', async () => {
    vi.spyOn(assistantApi, 'status').mockResolvedValue(STATUS);
    const reply = vi.spyOn(assistantApi, 'reply').mockResolvedValue({
      text: 'Gracias, confirmo el viernes.',
      input_truncated: false,
      output_truncated: false,
    });
    const onReplyWith = renderAssistant();
    await userEvent.click(
      await screen.findByRole('button', { name: t('webmail.assistant.reply') }),
    );
    const field = screen.getByLabelText(t('webmail.assistant.instructions'));
    await userEvent.type(field, 'demasiado largo');
    expect(
      screen.getByText(t('webmail.assistant.instructionsTooLong', { max: 10 })),
    ).toBeInTheDocument();
    expect(screen.getByRole('button', { name: t('webmail.assistant.generate') })).toBeDisabled();
    await userEvent.clear(field);
    await userEvent.type(field, 'acepta');
    await userEvent.click(screen.getByRole('button', { name: t('webmail.assistant.generate') }));
    expect(reply).toHaveBeenCalledWith({ folder: 'INBOX', uid: 5 }, 'acepta');
    await screen.findByText('Gracias, confirmo el viernes.');
    expect(onReplyWith).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole('button', { name: t('webmail.assistant.replyWith') }));
    expect(onReplyWith).toHaveBeenCalledWith('Gracias, confirmo el viernes.');
  });

  it('extrae citas y solo crea el evento tras confirmarlo', async () => {
    vi.spyOn(assistantApi, 'status').mockResolvedValue(STATUS);
    const extract = vi.spyOn(assistantApi, 'extract').mockResolvedValue({
      tasks: [{ title: 'Enviar contrato', due_date: '' }],
      events: [
        {
          title: 'Visita',
          date: '2026-09-25',
          start_time: '10:00',
          end_time: '',
          location: '',
          notes: '',
        },
      ],
      input_truncated: false,
    });
    const create = vi
      .spyOn(webmailApi, 'createCalendarEvent')
      .mockResolvedValue({ id: 'e1', etag: '"1"' } as CalendarEvent);
    renderAssistant();
    await userEvent.click(
      await screen.findByRole('button', { name: t('webmail.assistant.extract') }),
    );
    expect(extract).toHaveBeenCalledWith(
      { folder: 'INBOX', uid: 5 },
      expect.stringMatching(/^\d{4}-\d{2}-\d{2}$/),
    );
    expect(await screen.findByText('Visita')).toBeInTheDocument();
    expect(screen.getByText('Enviar contrato')).toBeInTheDocument();
    const add = screen.getAllByRole('button', {
      name: t('webmail.assistant.extract.addToCalendar'),
    });
    expect(add).toHaveLength(1);
    await userEvent.click(add[0]!);
    expect(create).not.toHaveBeenCalled();
    const dialog = await screen.findByRole('dialog');
    await userEvent.click(
      within(dialog).getByRole('button', { name: t('webmail.assistant.extract.addToCalendar') }),
    );
    await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
    const input = create.mock.calls[0]![0];
    expect(input).toMatchObject({ title: 'Visita', all_day: false, recurrence: null });
    expect(new Date(input.end).getTime() - new Date(input.start).getTime()).toBe(3_600_000);
  });

  it('muestra el error del servicio, como el tope diario', async () => {
    vi.spyOn(assistantApi, 'status').mockResolvedValue(STATUS);
    vi.spyOn(assistantApi, 'summarize').mockRejectedValue(
      new ApiError(429, {
        code: 'ASSISTANT_QUOTA_EXCEEDED',
        message: 'alcanzaste el maximo de peticiones al asistente de hoy',
      }),
    );
    renderAssistant();
    await userEvent.click(
      await screen.findByRole('button', { name: t('webmail.assistant.summarize') }),
    );
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'alcanzaste el maximo de peticiones al asistente de hoy',
    );
  });
});
