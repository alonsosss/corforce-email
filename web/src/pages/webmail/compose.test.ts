import { describe, expect, it } from 'vitest';
import { ApiError, ERROR_CODES } from '@/api/errors';
import type { MailMessage, MessagePart, SenderIdentity, WebmailMeta } from '@/api/webmail';
import { formatBytes } from '@/lib/quota';
import { t } from '@/i18n';
import {
  buildDraft,
  composeErrorMessage,
  composeProblems,
  htmlToPlainText,
  identityLabel,
  messagePlainText,
  normalizeRecipient,
  parseComposeMode,
  pickSender,
  prefixedSubject,
  quote,
  sendSignature,
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
    expect(draft.fromCandidates).toEqual(['luis@cliente.com']);
    expect(draft.source).toBeUndefined();
  });

  it('reenviar y seguir un borrador llevan los adjuntos del servidor, sin las imagenes en linea', () => {
    const pdf = part({ part: '2', filename: 'informe.pdf' });
    const logo = part({
      part: '1.2',
      content_id: 'logo@x',
      inline: true,
      content_type: 'image/png',
    });
    const withParts = message({
      folder: 'INBOX',
      uid: 42,
      html: '<p>Hola</p><img src="/api/v1/webmail/folders/INBOX/messages/42/parts/1.2">',
      attachments: [pdf, logo],
    });
    expect(buildDraft('forward', withParts, OWN).source).toEqual({
      folder: 'INBOX',
      uid: 42,
      parts: [pdf],
    });
    expect(buildDraft('draft', withParts, OWN).source?.parts).toEqual([pdf]);
    expect(buildDraft('reply', withParts, OWN).source).toBeUndefined();
  });

  describe('con un original en HTML', () => {
    const LOGO_URL = '/api/v1/webmail/folders/INBOX/messages/42/parts/1.2';
    const DATA = 'data:image/png;base64,AAAA';
    const pdf = part({ part: '2', filename: 'informe.pdf' });
    const logo = part({
      part: '1.2',
      content_id: 'logo@x',
      inline: true,
      content_type: 'image/png',
    });
    const original = message({
      html: `<p>Hola <b>equipo</b></p><img src="${LOGO_URL}">`,
      attachments: [pdf, logo],
    });
    const images = new Map([[LOGO_URL, DATA]]);

    it('responder cita el HTML con sus imagenes incrustadas y conserva la cita en texto', () => {
      const draft = buildDraft('reply', original, OWN, images);
      const doc = new DOMParser().parseFromString(draft.html ?? '', 'text/html');
      const quoteBlock = doc.querySelector('blockquote');
      expect(quoteBlock?.querySelector('b')?.textContent).toBe('equipo');
      expect(quoteBlock?.querySelector('img')?.getAttribute('src')).toBe(DATA);
      expect(doc.body.textContent).toContain('Luis');
      expect(draft.text).toContain('> Linea 1');
      expect(draft.source).toBeUndefined();
    });

    it('reenviar lleva la cabecera y el original con sus imagenes, sin volver a adjuntarlas', () => {
      const draft = buildDraft('forward', original, OWN, images);
      expect(draft.html).toContain(t('webmail.compose.forwardHeader'));
      expect(draft.html).toContain(`src="${DATA}"`);
      expect(draft.html).not.toContain(LOGO_URL);
      expect(draft.source?.parts).toEqual([pdf]);
    });

    it('seguir un borrador incrusta sus imagenes y no las adjunta otra vez', () => {
      const draft = buildDraft('draft', original, OWN, images);
      expect(draft.html).toContain(`src="${DATA}"`);
      expect(draft.source?.parts).toEqual([pdf]);
    });

    it('un original solo en texto se sigue citando linea a linea', () => {
      expect(buildDraft('reply', message(), OWN, images).html).toBeUndefined();
      expect(buildDraft('forward', message(), OWN, images).html).toBeUndefined();
    });
  });

  it('responder propone como remitente la direccion a la que llego el original', () => {
    const draft = buildDraft(
      'reply',
      message({ to: [{ name: '', email: 'Ventas@empresa.com' }], cc: [] }),
      OWN,
    );
    expect(draft.fromCandidates).toEqual(['Ventas@empresa.com']);
  });
});

function part(overrides: Partial<MessagePart> = {}): MessagePart {
  return {
    part: '2',
    filename: '',
    content_type: 'application/pdf',
    size: 1000,
    content_id: '',
    inline: false,
    ...overrides,
  };
}

const IDENTITIES: SenderIdentity[] = [
  { email: OWN, name: 'Ana', primary: true },
  { email: 'ventas@empresa.com', name: '', primary: false },
];

describe('remitente', () => {
  it('elige el primer candidato que sea remitente del buzon, sin distinguir mayusculas', () => {
    expect(pickSender(IDENTITIES, ['luis@cliente.com', 'VENTAS@empresa.com'])).toBe(
      'ventas@empresa.com',
    );
  });

  it('sin candidato valido es el propio buzon; sin lista, vacio', () => {
    expect(pickSender(IDENTITIES, ['otro@empresa.com'])).toBe(OWN);
    expect(pickSender([], [OWN])).toBe('');
  });

  it('muestra el nombre si lo hay', () => {
    expect(identityLabel(IDENTITIES[0]!)).toBe(`Ana <${OWN}>`);
    expect(identityLabel(IDENTITIES[1]!)).toBe('ventas@empresa.com');
  });
});

const LIMITS: WebmailMeta['limits'] = {
  max_recipients: 2,
  max_message_bytes: 1000,
  max_attachments: 2,
  max_download_bytes: 2000,
  max_body_part_bytes: 500,
  max_subject_chars: 5,
  max_search_bytes: 10,
  max_folder_name_bytes: 20,
  max_batch_uids: 500,
  max_scheduled_days: 30,
  max_thread_messages: 200,
  max_reminder_days: 30,
};

const CHECK = {
  to: ['a@x.com'],
  cc: [],
  bcc: [],
  subject: 'Hola',
  text: 'cuerpo',
  files: [] as File[],
  serverParts: [] as MessagePart[],
};

describe('topes del servicio antes de enviar', () => {
  it('sin meta no se comprueba nada: el servicio lo hace siempre', () => {
    expect(composeProblems({ ...CHECK, to: ['a@x.com', 'b@x.com', 'c@x.com'] }, null)).toEqual({});
  });

  it('cuenta destinatarios distintos como el servicio', () => {
    expect(
      composeProblems({ ...CHECK, to: ['a@x.com', 'A@X.com'], cc: ['b@x.com'] }, LIMITS),
    ).toEqual({});
    expect(
      composeProblems({ ...CHECK, to: ['a@x.com'], cc: ['b@x.com'], bcc: ['c@x.com'] }, LIMITS)
        .recipients,
    ).toBe(t('webmail.compose.tooManyRecipients', { n: 3, max: 2 }));
  });

  it('el asunto se mide en caracteres', () => {
    expect(composeProblems({ ...CHECK, subject: 'año!!' }, LIMITS).subject).toBeUndefined();
    expect(composeProblems({ ...CHECK, subject: 'seis!!' }, LIMITS).subject).toBe(
      t('webmail.compose.subjectTooLong', { max: 5 }),
    );
  });

  it('adjuntos subidos y del servidor cuentan juntos en numero y en tamano', () => {
    const file = new File(['x'.repeat(600)], 'a.txt');
    expect(
      composeProblems(
        { ...CHECK, files: [file], serverParts: [part(), part({ part: '3' })] },
        LIMITS,
      ).attachments,
    ).toBe(t('webmail.compose.tooManyAttachments', { n: 3, max: 2 }));
    // Asunto (4) y texto (6) en UTF-8, el fichero (600) y la parte del servidor (500).
    const big = composeProblems(
      { ...CHECK, files: [file], serverParts: [part({ size: 500 })] },
      LIMITS,
    );
    expect(big.attachments).toBe(
      t('webmail.compose.tooLarge', { size: formatBytes(1110), max: formatBytes(1000) }),
    );
    expect(composeProblems({ ...CHECK, serverParts: [part({ size: 900 })] }, LIMITS)).toEqual({});
  });
});

describe('clave de idempotencia', () => {
  const input = {
    to: ['a@x.com'],
    cc: [],
    bcc: [],
    subject: 'Hola',
    text: 'cuerpo',
    attachments: [new File(['hola'], 'nota.txt', { lastModified: 1 })],
  };

  it('el mismo contenido da la misma huella; otro contenido u otro borrador, otra', () => {
    expect(sendSignature(input, 5)).toBe(sendSignature({ ...input }, 5));
    expect(sendSignature({ ...input, text: 'otro' }, 5)).not.toBe(sendSignature(input, 5));
    expect(sendSignature(input, 6)).not.toBe(sendSignature(input, 5));
    expect(
      sendSignature({
        ...input,
        attachments: [new File(['hola!'], 'nota.txt', { lastModified: 1 })],
      }),
    ).not.toBe(sendSignature(input));
  });
});

describe('error del envio', () => {
  it('nombra al destinatario que rechazo el servidor', () => {
    const err = new ApiError(
      422,
      { code: ERROR_CODES.RECIPIENT_REJECTED, message: 'x' },
      { error: { code: 'RECIPIENT_REJECTED', message: 'x', details: { address: 'nadie@x.com' } } },
    );
    expect(composeErrorMessage(err)).toBe(
      t('webmail.compose.recipientRejected', { address: 'nadie@x.com' }),
    );
    const bare = new ApiError(422, { code: ERROR_CODES.RECIPIENT_REJECTED, message: 'x' }, {});
    expect(composeErrorMessage(bare)).toBe(t('error.code.RECIPIENT_REJECTED'));
  });
});
