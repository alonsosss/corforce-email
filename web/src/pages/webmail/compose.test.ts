import { describe, expect, it } from 'vitest';
import type { MailMessage } from '@/api/webmail';
import { t } from '@/i18n';
import {
  buildDraft,
  htmlToPlainText,
  messagePlainText,
  normalizeRecipient,
  parseComposeMode,
  prefixedSubject,
  quote,
} from './compose';

const OWN = 'ana@empresa.com';

function message(overrides: Partial<MailMessage> = {}): MailMessage {
  return {
    uid: 42,
    folder: 'INBOX',
    from: [{ name: 'Luis', email: 'luis@cliente.com' }],
    to: [
      { name: '', email: OWN },
      { name: 'Eva', email: 'eva@empresa.com' },
    ],
    cc: [{ name: '', email: 'jefe@cliente.com' }],
    bcc: [],
    reply_to: [],
    subject: 'Pedido',
    date: '2026-09-01T10:00:00Z',
    flags: [],
    size: 100,
    has_attachments: false,
    message_id: 'x@cliente.com',
    in_reply_to: [],
    references: [],
    text: 'Linea 1\nLinea 2',
    text_truncated: false,
    html: '',
    html_truncated: false,
    remote_images: { present: false, blocked: false },
    attachments: [],
    ...overrides,
  };
}

describe('direcciones de la redaccion', () => {
  it('normaliza como el servicio: dominio en minusculas y parte local intacta', () => {
    expect(normalizeRecipient(' Ana.Perez@Empresa.COM ')).toBe('Ana.Perez@empresa.com');
    expect(normalizeRecipient('<x@y.com>')).toBe('x@y.com');
  });

  it('rechaza lo que el servicio rechazaria, incluida la inyeccion de cabeceras', () => {
    expect(normalizeRecipient('sin-arroba')).toBeNull();
    expect(normalizeRecipient('a..b@x.com')).toBeNull();
    expect(normalizeRecipient('.a@x.com')).toBeNull();
    expect(normalizeRecipient('a@localhost')).toBeNull();
    expect(normalizeRecipient('a@x.com\r\nBcc: b@y.com')).toBeNull();
    expect(normalizeRecipient(`a@${'x'.repeat(250)}.com`)).toBeNull();
  });
});

describe('asunto y cita', () => {
  it('no repite el prefijo si el asunto ya lo lleva', () => {
    expect(prefixedSubject('Re:', 'Pedido')).toBe('Re: Pedido');
    expect(prefixedSubject('Re:', 'RE: Pedido')).toBe('RE: Pedido');
    expect(prefixedSubject('Fwd:', '')).toBe('Fwd:');
  });

  it('cita linea a linea', () => {
    expect(quote('uno\n\ndos')).toBe('> uno\n>\n> dos');
  });

  it('saca texto de un HTML con saltos en los bloques y sin scripts', () => {
    expect(htmlToPlainText('<p>Hola</p><p>Adios <b>ya</b></p><script>x()</script>')).toBe(
      'Hola\n\nAdios ya',
    );
    expect(messagePlainText({ text: '', html: '<div>Solo HTML</div>' })).toBe('Solo HTML');
    expect(messagePlainText({ text: 'Texto', html: '<p>HTML</p>' })).toBe('Texto');
  });

  it('solo acepta los modos conocidos', () => {
    expect(parseComposeMode('replyAll')).toBe('replyAll');
    expect(parseComposeMode('otro')).toBeNull();
    expect(parseComposeMode(null)).toBeNull();
  });
});

describe('borrador inicial', () => {
  it('responder va a quien envio, con Re:, la cita y el mensaje original', () => {
    const draft = buildDraft('reply', message(), OWN);
    expect(draft.to).toEqual(['luis@cliente.com']);
    expect(draft.cc).toEqual([]);
    expect(draft.subject).toBe('Re: Pedido');
    expect(draft.inReplyTo).toEqual({ folder: 'INBOX', uid: 42 });
    expect(draft.text).toContain('> Linea 1');
    expect(draft.text).toContain('Luis <luis@cliente.com>');
  });

  it('responder respeta Reply-To', () => {
    const draft = buildDraft(
      'reply',
      message({ reply_to: [{ name: '', email: 'pedidos@cliente.com' }] }),
      OWN,
    );
    expect(draft.to).toEqual(['pedidos@cliente.com']);
  });

  it('responder a todos quita al propio buzon, sin distinguir mayusculas, y no repite', () => {
    const draft = buildDraft(
      'replyAll',
      message({
        cc: [
          { name: '', email: 'jefe@cliente.com' },
          { name: '', email: 'ANA@empresa.com' },
        ],
      }),
      OWN,
    );
    expect(draft.to).toEqual(['luis@cliente.com', 'eva@empresa.com']);
    expect(draft.cc).toEqual(['jefe@cliente.com']);
  });

  it('responder a todos a un mensaje propio vuelve a quien lo envio', () => {
    const own = [{ name: '', email: OWN }];
    const draft = buildDraft('replyAll', message({ from: own, to: own, cc: [] }), OWN);
    expect(draft.to).toEqual([OWN]);
  });

  it('reenviar no tiene destinatarios ni encadena respuesta, y lleva el original sin citar', () => {
    const draft = buildDraft('forward', message(), OWN);
    expect(draft.to).toEqual([]);
    expect(draft.inReplyTo).toBeUndefined();
    expect(draft.subject).toBe('Fwd: Pedido');
    expect(draft.text).toContain(t('webmail.compose.forwardHeader'));
    expect(draft.text).toContain('\nLinea 1\nLinea 2');
  });

  it('seguir un borrador conserva destinatarios y asunto y lo reemplaza al guardar', () => {
    const draft = buildDraft(
      'draft',
      message({ bcc: [{ name: '', email: 'oculto@empresa.com' }] }),
      OWN,
    );
    expect(draft.to).toEqual([OWN, 'eva@empresa.com']);
    expect(draft.bcc).toEqual(['oculto@empresa.com']);
    expect(draft.subject).toBe('Pedido');
    expect(draft.text).toBe('Linea 1\nLinea 2');
    expect(draft.draftUid).toBe(42);
    expect(draft.inReplyTo).toBeUndefined();
  });
});
