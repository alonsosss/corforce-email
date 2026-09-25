import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { assistantApi, type AssistantStatus } from '@/api/webmailAssistant';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import { ComposeAssistant, type ComposeAssistantProps } from './ComposeAssistant';

const STATUS: AssistantStatus = {
  available: true,
  actions: ['summarize', 'reply', 'tone', 'extract'],
  tones: ['formal', 'friendly', 'brief'],
  limits: {
    max_input_chars: 20,
    max_thread_messages: 5,
    max_instruction_chars: 10,
    mailbox_daily: 50,
    tenant_daily: 500,
  },
  usage: { mailbox: 0, tenant: 0 },
};

function renderCompose(props: Partial<ComposeAssistantProps> = {}) {
  const all: ComposeAssistantProps = {
    bodyText: () => 'te mando el informe',
    onInsert: vi.fn(),
    onReplace: vi.fn(),
    ...props,
  };
  render(
    <MemoryRouter>
      <ToastProvider>
        <ComposeAssistant {...all} />
      </ToastProvider>
    </MemoryRouter>,
  );
  return all;
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('asistente en la redaccion', () => {
  it('cambia el tono del borrador y solo lo sustituye si el usuario lo pide', async () => {
    vi.spyOn(assistantApi, 'status').mockResolvedValue(STATUS);
    const tone = vi.spyOn(assistantApi, 'tone').mockResolvedValue({
      text: 'Le remito el informe.',
      input_truncated: false,
      output_truncated: false,
    });
    const props = renderCompose();
    await userEvent.click(
      await screen.findByRole('button', { name: t('webmail.assistant.tone.formal') }),
    );
    expect(tone).toHaveBeenCalledWith('te mando el informe', 'formal');
    await screen.findByText('Le remito el informe.');
    expect(props.onReplace).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole('button', { name: t('webmail.assistant.replace') }));
    expect(props.onReplace).toHaveBeenCalledWith('Le remito el informe.');
    expect(screen.queryByText('Le remito el informe.')).not.toBeInTheDocument();
  });

  it('no envia un cuerpo vacio ni uno por encima del tope servido', async () => {
    vi.spyOn(assistantApi, 'status').mockResolvedValue(STATUS);
    const tone = vi.spyOn(assistantApi, 'tone');
    let body = '   ';
    renderCompose({ bodyText: () => body });
    const brief = await screen.findByRole('button', { name: t('webmail.assistant.tone.brief') });
    await userEvent.click(brief);
    expect(screen.getByRole('alert')).toHaveTextContent(t('webmail.assistant.toneEmpty'));
    body = 'x'.repeat(21);
    await userEvent.click(brief);
    expect(screen.getByRole('alert')).toHaveTextContent(
      t('webmail.assistant.toneTooLong', { max: 20 }),
    );
    expect(tone).not.toHaveBeenCalled();
  });

  it('en una respuesta propone el cuerpo y lo inserta delante', async () => {
    vi.spyOn(assistantApi, 'status').mockResolvedValue(STATUS);
    const reply = vi.spyOn(assistantApi, 'reply').mockResolvedValue({
      text: 'Confirmado.',
      input_truncated: false,
      output_truncated: false,
    });
    const props = renderCompose({ replyTo: { folder: 'INBOX', uid: 9 } });
    await userEvent.click(
      await screen.findByRole('button', { name: t('webmail.assistant.reply') }),
    );
    expect(reply).toHaveBeenCalledWith({ folder: 'INBOX', uid: 9 }, '');
    await userEvent.click(
      await screen.findByRole('button', { name: t('webmail.assistant.insert') }),
    );
    expect(props.onInsert).toHaveBeenCalledWith('Confirmado.');
  });

  it('inserta una sola vez la respuesta que llega del lector, aunque la empresa no lo ofrezca ya', async () => {
    const status = vi
      .spyOn(assistantApi, 'status')
      .mockResolvedValue({ ...STATUS, available: false, reason: 'disabled' });
    const props = renderCompose({ initialText: 'Texto del lector' });
    await waitFor(() => expect(status).toHaveBeenCalled());
    expect(props.onInsert).toHaveBeenCalledTimes(1);
    expect(props.onInsert).toHaveBeenCalledWith('Texto del lector');
    expect(screen.queryByText(t('webmail.assistant.tone.label'))).not.toBeInTheDocument();
  });
});
