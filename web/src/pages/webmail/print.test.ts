import { describe, expect, it } from 'vitest';
import type { MailMessage } from '@/api/webmail';
import { buildPrintDocument } from './print';

const PART_URL = '/api/v1/webmail/folders/INBOX/messages/7/parts/2';
const DATA = 'data:image/png;base64,AAAA';

const MESSAGE: MailMessage = {
  uid: 7,
  folder: 'INBOX',
  from: [{ name: 'Luis', email: 'luis@cliente.com' }],
  to: [{ name: '', email: 'ana@empresa.com' }],
  cc: [],
  bcc: [],
  reply_to: [],
  subject: 'Logo',
  date: '2026-09-01T10:00:00Z',
  flags: [],
  size: 100,
  has_attachments: true,
  message_id: 'x@cliente.com',
  in_reply_to: [],
  references: [],
  text: '',
  text_truncated: false,
  html: `<p>Hola</p><img src="${PART_URL}" alt="logo">`,
  html_truncated: false,
  remote_images: { present: false, blocked: false },
  attachments: [],
};

describe('impresion', () => {
  it('incluye las imagenes en linea ya descargadas para la lectura', () => {
    const html = buildPrintDocument(MESSAGE, {
      allowRemoteImages: false,
      inlineImages: new Map([[PART_URL, DATA]]),
    });
    const doc = new DOMParser().parseFromString(html, 'text/html');
    expect(doc.querySelector('img')?.getAttribute('src')).toBe(DATA);
  });

  it('sin ellas no pide la parte al servidor desde el marco', () => {
    const html = buildPrintDocument(MESSAGE, { allowRemoteImages: false });
    expect(html).not.toContain(PART_URL);
  });
});
