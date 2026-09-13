import { afterEach, describe, expect, it, vi } from 'vitest';
import { setAccessToken } from './client';
import { endpoints } from './endpoints';
import { ApiError, ERROR_CODES } from './errors';
import {
  composeFormData,
  filenameFromDisposition,
  onWebmailSessionExpired,
  webmailApi,
} from './webmail';

interface Call {
  url: string;
  init: RequestInit;
}

function mockFetch(handler: (call: Call) => Response): Call[] {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const call = { url: String(input), init: init ?? {} };
      calls.push(call);
      return handler(call);
    }),
  );
  return calls;
}

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const SESSION = {
  username: 'ana@empresa.com',
  display_name: 'Ana',
  expires_at: '2026-09-13T20:00:00Z',
  idle_timeout_seconds: 1800,
  quota: null,
};

describe('cliente del webmail', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    setAccessToken(null);
  });

  it('va con la cookie del servicio y nunca con el access token de la plataforma', async () => {
    setAccessToken('token-de-la-plataforma');
    const calls = mockFetch(() => json(200, { data: [] }));

    await webmailApi.folders();

    expect(calls).toHaveLength(1);
    expect(calls[0]?.url).toBe(endpoints.webmail.folders);
    expect(calls[0]?.init.credentials).toBe('include');
    const headers = (calls[0]?.init.headers ?? {}) as Record<string, string>;
    expect(headers.Authorization).toBeUndefined();
    expect(JSON.stringify(calls[0]?.init)).not.toContain('token-de-la-plataforma');
  });

  it('inicia sesion con buzon y contrasena en JSON y no guarda nada en el navegador', async () => {
    const setItem = vi.spyOn(Storage.prototype, 'setItem');
    const calls = mockFetch(() => json(200, { data: SESSION }));

    const session = await webmailApi.login('ana@empresa.com', 'secreta');

    expect(session.username).toBe('ana@empresa.com');
    expect(calls[0]?.init.method).toBe('POST');
    expect(calls[0]?.url).toBe(endpoints.webmail.session);
    expect(JSON.parse(String(calls[0]?.init.body))).toEqual({
      username: 'ana@empresa.com',
      password: 'secreta',
    });
    expect(setItem).not.toHaveBeenCalled();
  });

  it('un SESSION_EXPIRED avisa a quien escucha y llega con su codigo', async () => {
    const listener = vi.fn();
    const off = onWebmailSessionExpired(listener);
    mockFetch(() =>
      json(401, { error: { code: 'SESSION_EXPIRED', message: 'sesion invalida o caducada' } }),
    );

    await expect(webmailApi.folders()).rejects.toMatchObject({
      status: 401,
      code: ERROR_CODES.SESSION_EXPIRED,
    });
    expect(listener).toHaveBeenCalledTimes(1);
    off();
  });

  it('el 429 del limitador del gateway, sin envelope del API, es RATE_LIMITED', async () => {
    mockFetch(() => new Response('{"error":"rate limit exceeded"}', { status: 429 }));
    const err = await webmailApi.login('a@b.com', 'x').catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).code).toBe(ERROR_CODES.RATE_LIMITED);
  });

  it('una respuesta que no es JSON no se ensena: INVALID_RESPONSE con el estado', async () => {
    mockFetch(() => new Response('<html>Bad Gateway</html>', { status: 502 }));
    const err = await webmailApi.folders().catch((e: unknown) => e);
    expect((err as ApiError).status).toBe(502);
    expect((err as ApiError).code).toBe(ERROR_CODES.INVALID_RESPONSE);
    expect((err as ApiError).message).not.toContain('Bad Gateway');
  });

  it('codifica la carpeta entera y pide las imagenes remotas solo cuando se piden', async () => {
    const calls = mockFetch(() => json(200, { data: {} }));

    await webmailApi.message('INBOX/Proyectos', 7);
    await webmailApi.message('INBOX/Proyectos', 7, { peek: true, allowRemoteImages: true });

    expect(calls[0]?.url).toBe('/api/v1/webmail/folders/INBOX%2FProyectos/messages/7');
    expect(calls[1]?.url).toBe(
      '/api/v1/webmail/folders/INBOX%2FProyectos/messages/7?peek=true&remote_images=allow',
    );
  });

  it('una pagina del listado toma el tamano y el total del meta del servicio', async () => {
    mockFetch(() =>
      json(200, {
        data: [{ uid: 1 }],
        meta: { page: 2, per_page: 50, total: 51, total_pages: 2 },
      }),
    );
    const page = await webmailApi.messages('INBOX', { page: 2, search: 'factura' });
    expect(page).toMatchObject({ page: 2, perPage: 50, total: 51, totalPages: 2 });
  });

  it('descarga una parte como bytes, con el nombre ya saneado por el servicio', async () => {
    mockFetch(
      () =>
        new Response('%PDF', {
          status: 200,
          headers: {
            'Content-Type': 'application/pdf',
            'Content-Disposition': "attachment; filename*=utf-8''informe%20final.pdf",
          },
        }),
    );
    const part = await webmailApi.downloadPart('INBOX', 3, '2');
    expect(part.filename).toBe('informe final.pdf');
    expect(part.contentType).toBe('application/pdf');
    expect(await part.blob.text()).toBe('%PDF');
  });

  it('envia multipart sin fijar Content-Type: la frontera la pone el navegador', async () => {
    const calls = mockFetch(() => json(202, { data: { message_id: 'm@x', saved_to_sent: true } }));
    const result = await webmailApi.send({
      to: ['a@x.com'],
      cc: [],
      bcc: [],
      subject: 'Hola',
      text: 'Cuerpo',
      attachments: [],
    });
    expect(result.saved_to_sent).toBe(true);
    expect(calls[0]?.init.body).toBeInstanceOf(FormData);
    const headers = (calls[0]?.init.headers ?? {}) as Record<string, string>;
    expect(headers['Content-Type']).toBeUndefined();
  });
});

describe('formulario de redaccion', () => {
  it('cada campo de direcciones viaja como una lista y el borrador anterior se reemplaza', () => {
    const file = new File(['hola'], 'nota.txt', { type: 'text/plain' });
    const form = composeFormData(
      {
        to: ['a@x.com', 'b@y.com'],
        cc: [],
        bcc: ['c@z.com'],
        subject: 'Hola',
        text: 'Cuerpo',
        inReplyTo: { folder: 'INBOX', uid: 9 },
        attachments: [file],
      },
      12,
    );
    expect(form.getAll('to')).toEqual(['a@x.com, b@y.com']);
    expect(form.has('cc')).toBe(false);
    expect(form.get('bcc')).toBe('c@z.com');
    expect(form.get('in_reply_to')).toBe('9');
    expect(form.get('in_reply_to_folder')).toBe('INBOX');
    expect(form.get('replace_uid')).toBe('12');
    expect((form.get('attachments') as File).name).toBe('nota.txt');
  });

  it('sin borrador anterior no manda replace_uid, que send rechazaria', () => {
    const form = composeFormData({
      to: ['a@x.com'],
      cc: [],
      bcc: [],
      subject: '',
      text: '',
      attachments: [],
    });
    expect(form.has('replace_uid')).toBe(false);
    expect(form.has('in_reply_to')).toBe(false);
  });
});

describe('nombre de fichero de Content-Disposition', () => {
  it('prefiere filename* (UTF-8) y entiende el filename entre comillas', () => {
    const accented = `a${String.fromCharCode(0xf1)}o.pdf`;
    expect(filenameFromDisposition("attachment; filename*=utf-8''a%C3%B1o.pdf")).toBe(accented);
    expect(filenameFromDisposition('attachment; filename="a \\"b\\".txt"')).toBe('a "b".txt');
    expect(filenameFromDisposition('attachment; filename=plano.txt')).toBe('plano.txt');
    expect(filenameFromDisposition('attachment')).toBeNull();
    expect(filenameFromDisposition(null)).toBeNull();
  });
});
