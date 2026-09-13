import { afterEach, describe, expect, it, vi } from 'vitest';
import { webmailApi, type MessagePart } from '@/api/webmail';
import { loadInlineImages, referencedInlineParts } from './inlineImages';

function part(id: string, contentId: string): MessagePart {
  return {
    part: id,
    filename: '',
    content_type: 'image/png',
    size: 10,
    content_id: contentId,
    inline: Boolean(contentId),
  };
}

const LOGO = '/api/v1/webmail/folders/INBOX%2FClientes/messages/7/parts/2';
const MESSAGE = {
  uid: 7,
  folder: 'INBOX/Clientes',
  html: `<p>x</p><img src="${LOGO}"><img src="https://tracker.test/p.gif"><img src="/api/v1/webmail/folders/INBOX%2FClientes/messages/7/parts/9"><img src="/api/v1/webmail/folders/INBOX%2FClientes/messages/8/parts/3">`,
  attachments: [part('2', 'logo@x'), part('3', ''), part('3.1', 'otro@x')],
};

describe('imagenes en linea del correo', () => {
  afterEach(() => vi.restoreAllMocks());

  it('reconoce solo las partes en linea de este mensaje que el HTML referencia', () => {
    const refs = referencedInlineParts(MESSAGE);
    expect([...refs.keys()]).toEqual([LOGO]);
    expect(refs.get(LOGO)?.part).toBe('2');
  });

  it('las incrusta como data: de mapa de bits con la sesion del buzon', async () => {
    const download = vi.spyOn(webmailApi, 'downloadPart').mockResolvedValue({
      blob: new Blob(['GIF89a']),
      filename: null,
      contentType: 'image/gif',
    });
    const images = await loadInlineImages(MESSAGE, referencedInlineParts(MESSAGE));
    expect(download).toHaveBeenCalledWith('INBOX/Clientes', 7, '2', undefined);
    expect(images.get(LOGO)).toMatch(/^data:image\/gif;base64,/);
  });

  it('no incrusta lo que el servicio no entrega como mapa de bits (un SVG llega como octet-stream)', async () => {
    vi.spyOn(webmailApi, 'downloadPart').mockResolvedValue({
      blob: new Blob(['<svg onload="x()"/>']),
      filename: null,
      contentType: 'application/octet-stream',
    });
    const images = await loadInlineImages(MESSAGE, referencedInlineParts(MESSAGE));
    expect(images.size).toBe(0);
  });

  it('una parte que falla no rompe la lectura del mensaje', async () => {
    vi.spyOn(webmailApi, 'downloadPart').mockRejectedValue(new Error('red'));
    const images = await loadInlineImages(MESSAGE, referencedInlineParts(MESSAGE));
    expect(images.size).toBe(0);
  });
});
