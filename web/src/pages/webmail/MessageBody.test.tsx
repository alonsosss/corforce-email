import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ApiError } from '@/api/errors';
import { webmailApi, type MailMessage, type MessagePart } from '@/api/webmail';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import { resetWebmailCatalogs } from '@/webmail/catalogs';
import { MessageBody } from './MessageBody';
import { META } from './testing';

const PHOTO: MessagePart = {
  part: '2',
  filename: 'obra.png',
  content_type: 'image/png',
  size: 2048,
  content_id: '',
  inline: false,
};

function messageWith(attachments: MessagePart[]): MailMessage {
  return {
    uid: 5,
    folder: 'INBOX',
    from: [{ name: 'Luis', email: 'luis@cliente.com' }],
    to: [],
    cc: [],
    bcc: [],
    reply_to: [],
    subject: 'Fotos',
    date: '2026-09-01T10:00:00Z',
    flags: [],
    size: 100,
    has_attachments: true,
    message_id: 'f@cliente.com',
    in_reply_to: [],
    references: [],
    text: 'Adjunto las fotos',
    text_truncated: false,
    html: '',
    html_truncated: false,
    remote_images: { present: false, blocked: false },
    attachments,
  };
}

function renderBody(message: MailMessage, remoteAllowed = false) {
  return render(
    <ToastProvider>
      <MessageBody
        message={message}
        remoteAllowed={remoteAllowed}
        remoteLoading={false}
        onAllowRemote={() => undefined}
        inlineImages={new Map()}
      />
    </ToastProvider>,
  );
}

const png = () => ({
  blob: new Blob([new Uint8Array([137, 80, 78, 71])]),
  filename: 'obra.png',
  contentType: 'image/png',
});

describe('miniaturas de los adjuntos de imagen', () => {
  const created: Blob[] = [];
  const revoke = vi.fn();

  beforeEach(() => {
    resetWebmailCatalogs();
    created.length = 0;
    revoke.mockReset();
    Object.assign(URL, {
      createObjectURL: (blob: Blob) => {
        created.push(blob);
        return `blob:miniatura-${created.length}`;
      },
      revokeObjectURL: revoke,
    });
    vi.spyOn(webmailApi, 'meta').mockResolvedValue(META);
  });
  afterEach(() => vi.restoreAllMocks());

  it('pinta la miniatura, la abre en el visor y la descarga desde alli', async () => {
    const user = userEvent.setup();
    const download = vi.spyOn(webmailApi, 'downloadPart').mockResolvedValue(png());
    const { unmount } = renderBody(messageWith([PHOTO]));

    const open = await screen.findByRole('button', {
      name: t('webmail.attachments.view', { name: 'obra.png' }),
    });
    await waitFor(() => expect(open).toBeEnabled());
    expect(download).toHaveBeenCalledWith('INBOX', 5, '2', expect.any(AbortSignal));
    expect(open.querySelector('img')?.getAttribute('src')).toBe('blob:miniatura-1');
    expect(created[0]?.type).toBe('image/png');

    await user.click(open);
    const viewer = await screen.findByRole('dialog', { name: 'obra.png' });
    expect(within(viewer).getByRole('img', { name: 'obra.png' })).toHaveAttribute(
      'src',
      'blob:miniatura-1',
    );
    await user.click(
      within(viewer).getByRole('button', { name: t('webmail.attachments.download') }),
    );
    await waitFor(() => expect(download).toHaveBeenCalledTimes(2));
    expect(download.mock.calls[1]?.slice(0, 3)).toEqual(['INBOX', 5, '2']);

    unmount();
    expect(revoke).toHaveBeenCalledWith('blob:miniatura-1');
  });

  it('nunca pinta un SVG: ni declarado ni servido como tal', async () => {
    const download = vi.spyOn(webmailApi, 'downloadPart').mockResolvedValue({
      blob: new Blob(['<svg/>']),
      filename: 'falsa.png',
      contentType: 'image/svg+xml',
    });
    const svg: MessagePart = {
      ...PHOTO,
      part: '3',
      filename: 'logo.svg',
      content_type: 'image/svg+xml',
    };
    renderBody(messageWith([PHOTO, svg]));

    expect(
      await screen.findByRole('img', { name: t('webmail.attachments.previewUnavailable') }),
    ).toBeInTheDocument();
    expect(download).toHaveBeenCalledTimes(1);
    expect(created).toHaveLength(0);
    expect(document.querySelector('img')).toBeNull();
    expect(
      screen.getByRole('button', {
        name: t('webmail.attachments.downloadName', { name: 'logo.svg' }),
      }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: t('webmail.attachments.view', { name: 'logo.svg' }) }),
    ).toBeNull();
  });

  it('por encima del tope de la meta, o sin meta, queda como adjunto normal', async () => {
    const download = vi.spyOn(webmailApi, 'downloadPart').mockResolvedValue(png());
    const big: MessagePart = { ...PHOTO, size: META.limits.max_download_bytes + 1 };
    renderBody(messageWith([big]));
    expect(
      await screen.findByRole('button', {
        name: t('webmail.attachments.downloadName', { name: 'obra.png' }),
      }),
    ).toBeInTheDocument();
    await waitFor(() => expect(webmailApi.meta).toHaveBeenCalled());
    expect(
      screen.queryByRole('button', { name: t('webmail.attachments.view', { name: 'obra.png' }) }),
    ).toBeNull();
    expect(download).not.toHaveBeenCalled();
  });

  it('si la meta no se puede leer no pide ninguna miniatura', async () => {
    vi.mocked(webmailApi.meta).mockRejectedValue(new ApiError(503, null));
    const download = vi.spyOn(webmailApi, 'downloadPart').mockResolvedValue(png());
    renderBody(messageWith([PHOTO]));
    await waitFor(() => expect(webmailApi.meta).toHaveBeenCalled());
    expect(
      screen.getByRole('button', {
        name: t('webmail.attachments.downloadName', { name: 'obra.png' }),
      }),
    ).toBeInTheDocument();
    expect(download).not.toHaveBeenCalled();
  });
});

describe('imagenes remotas en el lector', () => {
  it('pedidas, el marco solo carga las del proxy del mismo origen', () => {
    const proxied = '/api/v1/webmail/image-proxy?u=eA&sig=f1';
    renderBody(
      {
        ...messageWith([]),
        html: `<p>Hola</p><img src="${proxied}"><img src="https://tracker.test/p.gif">`,
        remote_images: { present: true, blocked: false },
      },
      true,
    );
    const frame = document.querySelector('iframe');
    const doc = new DOMParser().parseFromString(frame?.getAttribute('srcdoc') ?? '', 'text/html');
    const origin = window.location.origin;
    expect([...doc.querySelectorAll('img')].map((img) => img.getAttribute('src'))).toEqual([
      `${origin}${proxied}`,
      null,
    ]);
    expect(
      doc.querySelector('meta[http-equiv="Content-Security-Policy"]')?.getAttribute('content'),
    ).toContain(`img-src data: ${origin};`);
  });
});
