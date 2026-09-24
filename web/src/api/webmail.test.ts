import { afterEach, describe, expect, it, vi } from 'vitest';
import { setAccessToken } from './client';
import { endpoints } from './endpoints';
import { ApiError, ERROR_CODES, errorDetail } from './errors';
import {
  composeFormData,
  filenameFromDisposition,
  onWebmailSessionExpired,
  openWebmailEvents,
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

  it('envia multipart con la clave de idempotencia y el borrador que retira', async () => {
    const calls = mockFetch(() =>
      json(202, {
        data: { message_id: 'm@x', saved_to_sent: true, draft_removed: true, replayed: false },
      }),
    );
    const result = await webmailApi.send(
      { to: ['a@x.com'], cc: [], bcc: [], subject: 'Hola', text: 'Cuerpo', attachments: [] },
      { idempotencyKey: 'clave-de-prueba-0001', replaceUid: 12 },
    );
    expect(result).toMatchObject({ saved_to_sent: true, draft_removed: true, replayed: false });
    expect(calls[0]?.url).toBe(endpoints.webmail.send);
    const body = calls[0]?.init.body;
    expect(body).toBeInstanceOf(FormData);
    expect((body as FormData).get('replace_uid')).toBe('12');
    const headers = (calls[0]?.init.headers ?? {}) as Record<string, string>;
    expect(headers['Idempotency-Key']).toBe('clave-de-prueba-0001');
    // Sin Content-Type: la frontera multipart la pone el navegador.
    expect(headers['Content-Type']).toBeUndefined();
  });

  it('lee la meta y los remitentes del servicio con la sesion del buzon', async () => {
    const calls = mockFetch((call) =>
      call.url === endpoints.webmail.meta
        ? json(200, { data: { limits: { max_recipients: 100 } } })
        : json(200, { data: [{ email: 'ana@empresa.com', name: 'Ana', primary: true }] }),
    );
    const meta = await webmailApi.meta();
    const identities = await webmailApi.identities();
    expect(meta.limits.max_recipients).toBe(100);
    expect(identities).toEqual([{ email: 'ana@empresa.com', name: 'Ana', primary: true }]);
    expect(calls.map((c) => c.url)).toEqual([endpoints.webmail.meta, endpoints.webmail.identities]);
    expect(calls.every((c) => c.init.credentials === 'include')).toBe(true);
  });

  it('un destinatario rechazado trae la direccion en error.details', async () => {
    mockFetch(() =>
      json(422, {
        error: {
          code: 'RECIPIENT_REJECTED',
          message: 'destinatario rechazado',
          details: { address: 'nadie@empresa.com' },
        },
      }),
    );
    const err = await webmailApi
      .send(
        { to: ['nadie@empresa.com'], cc: [], bcc: [], subject: '', text: '', attachments: [] },
        { idempotencyKey: 'clave-de-prueba-0002' },
      )
      .catch((e: unknown) => e);
    expect(errorDetail(err, 'address')).toBe('nadie@empresa.com');
    expect(errorDetail(err, 'field')).toBeNull();
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

  it('el remitente y los adjuntos del servidor viajan en sus campos, una parte por valor', () => {
    const form = composeFormData({
      from: 'ventas@empresa.com',
      to: ['a@x.com'],
      cc: [],
      bcc: [],
      subject: 'Fwd: Pedido',
      text: '',
      attachments: [],
      source: { folder: 'INBOX/Proyectos', uid: 42, parts: ['2', '1.3'] },
    });
    expect(form.get('from')).toBe('ventas@empresa.com');
    expect(form.get('source_folder')).toBe('INBOX/Proyectos');
    expect(form.get('source_uid')).toBe('42');
    expect(form.getAll('source_parts')).toEqual(['2', '1.3']);
    expect(form.has('attachments')).toBe(false);
  });

  it('sin borrador anterior ni origen no manda sus campos', () => {
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
    expect(form.has('from')).toBe(false);
    expect(form.has('source_folder')).toBe(false);
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

describe('flujo de avisos de la bandeja', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('abre el flujo del webmail con la cookie y sin ningun dato mas', () => {
    const created: { url: string; init?: EventSourceInit }[] = [];
    vi.stubGlobal(
      'EventSource',
      class {
        constructor(url: string, init?: EventSourceInit) {
          created.push({ url, init });
        }
      },
    );
    openWebmailEvents();
    expect(created).toHaveLength(1);
    expect(created[0]?.url).toBe(endpoints.webmail.events);
    expect(created[0]?.init).toEqual({ withCredentials: true });
  });
});

describe('respuesta automatica del buzon', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    setAccessToken(null);
  });

  const VACATION = {
    enabled: true,
    subject: 'Ausente',
    message: 'Vuelvo.',
    interval_days: 2,
    starts_on: null,
    ends_on: null,
    updated_at: null,
    limits: {
      subject_max_length: 200,
      message_max_length: 8192,
      interval_min_days: 1,
      interval_max_days: 30,
    },
  };

  it('se lee y se guarda por la ruta del webmail, con la cookie y sin el access token', async () => {
    setAccessToken('token-de-la-plataforma');
    const calls = mockFetch(() => json(200, { data: VACATION }));

    const read = await webmailApi.vacation();
    const saved = await webmailApi.setVacation({
      enabled: true,
      subject: 'Ausente',
      message: 'Vuelvo.',
      interval_days: 2,
      starts_on: null,
      ends_on: '2026-09-30',
    });

    expect(read.limits.message_max_length).toBe(8192);
    expect(saved.enabled).toBe(true);
    expect(calls[0]?.url).toBe(endpoints.webmail.vacation);
    expect(calls[0]?.init.method).toBe('GET');
    expect(calls[1]?.init.method).toBe('PUT');
    expect(JSON.parse(String(calls[1]?.init.body))).toEqual({
      enabled: true,
      subject: 'Ausente',
      message: 'Vuelvo.',
      interval_days: 2,
      starts_on: null,
      ends_on: '2026-09-30',
    });
    for (const call of calls) {
      expect(call.init.credentials).toBe('include');
      expect(JSON.stringify(call.init)).not.toContain('token-de-la-plataforma');
    }
  });

  it('un 422 del directorio llega como error con su mensaje', async () => {
    mockFetch(() =>
      json(422, { error: { code: 'VALIDATION_ERROR', message: 'el mensaje supera los 8192' } }),
    );
    await expect(
      webmailApi.setVacation({
        enabled: true,
        subject: '',
        message: 'x',
        interval_days: 1,
        starts_on: null,
        ends_on: null,
      }),
    ).rejects.toMatchObject({ status: 422 });
  });
});

describe('libreta de direcciones de la empresa', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    setAccessToken(null);
  });

  it('se consulta por la ruta del webmail con el texto codificado, la cookie y sin el access token', async () => {
    setAccessToken('token-de-la-plataforma');
    const calls = mockFetch(() =>
      json(200, { data: [{ address: 'ana@empresa.pe', display_name: 'Ana Diaz' }] }),
    );

    const found = await webmailApi.addressBook('ana & co');

    expect(found).toEqual([{ address: 'ana@empresa.pe', display_name: 'Ana Diaz' }]);
    expect(calls[0]?.url).toBe(`${endpoints.webmail.addressBook}?q=ana+%26+co`);
    expect(calls[0]?.init.method).toBe('GET');
    expect(calls[0]?.init.credentials).toBe('include');
    expect(JSON.stringify(calls[0]?.init)).not.toContain('token-de-la-plataforma');
  });

  it('una respuesta sin datos es una lista vacia', async () => {
    mockFetch(() => json(200, { data: null }));
    expect(await webmailApi.addressBook('')).toEqual([]);
  });
});

describe('reintento de lecturas del webmail', () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  const unavailable = () =>
    json(503, { error: { code: ERROR_CODES.SERVICE_UNAVAILABLE, message: 'imap' } });

  it('una lectura cortada por un reinicio se repite y el usuario no ve el fallo', async () => {
    vi.useFakeTimers();
    let n = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        n += 1;
        if (n === 1) throw new TypeError('Failed to fetch');
        if (n === 2) return unavailable();
        return json(200, { data: [] });
      }),
    );

    const result = webmailApi.folders();
    await vi.advanceTimersByTimeAsync(1000);
    await vi.advanceTimersByTimeAsync(3000);

    await expect(result).resolves.toEqual([]);
    expect(n).toBe(3);
  });

  it('si sigue sin responder tras los reintentos, llega el error de red', async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn(async () => {
      throw new TypeError('Failed to fetch');
    });
    vi.stubGlobal('fetch', fetchMock);

    const result = webmailApi.folders();
    const settled = expect(result).rejects.toMatchObject({ code: ERROR_CODES.NETWORK_ERROR });
    await vi.advanceTimersByTimeAsync(4000);
    await settled;
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it('no repite escrituras ni errores que esperar no arregla', async () => {
    const calls = mockFetch(() =>
      json(503, { error: { code: 'SCAN_UNAVAILABLE', message: 'clamav' } }),
    );
    await expect(webmailApi.folders()).rejects.toBeInstanceOf(ApiError);
    expect(calls).toHaveLength(1);

    const failing = vi.fn(async () => {
      throw new TypeError('Failed to fetch');
    });
    vi.stubGlobal('fetch', failing);
    await expect(webmailApi.logout()).rejects.toMatchObject({ code: ERROR_CODES.NETWORK_ERROR });
    expect(failing).toHaveBeenCalledTimes(1);
  });
});

describe('webmail competitivo: contrato del API', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  const headersOf = (call?: Call) => (call?.init.headers ?? {}) as Record<string, string>;

  it('actua sobre varios mensajes con un solo POST y el cuerpo del contrato', async () => {
    const calls = mockFetch(() => json(200, { data: { affected: 2, permanent: false } }));
    const result = await webmailApi.batch('INBOX', [1, 2], { action: 'move', to: 'Junk' });
    expect(result).toEqual({ affected: 2, permanent: false });
    expect(calls[0]?.url).toBe(endpoints.webmail.batch('INBOX'));
    expect(calls[0]?.init.method).toBe('POST');
    expect(JSON.parse(String(calls[0]?.init.body))).toEqual({
      uids: [1, 2],
      action: 'move',
      to: 'Junk',
    });
  });

  it('crea, renombra, borra y vacia carpetas con el nombre completo codificado', async () => {
    const calls = mockFetch((call) =>
      call.init.method === 'DELETE'
        ? new Response(null, { status: 204 })
        : json(call.url.endsWith('/empty') ? 200 : 201, {
            data: call.url.endsWith('/empty') ? { removed: 3 } : { name: 'Clientes/2026' },
          }),
    );
    await webmailApi.createFolder('Clientes/2026');
    await webmailApi.renameFolder('Clientes/2026', 'Clientes/2027');
    await webmailApi.deleteFolder('Clientes/2027');
    expect(await webmailApi.emptyFolder('Trash')).toEqual({ removed: 3 });
    expect(calls.map((c) => [c.init.method, c.url])).toEqual([
      ['POST', endpoints.webmail.folders],
      ['PATCH', '/api/v1/webmail/folders/Clientes%2F2026'],
      ['DELETE', '/api/v1/webmail/folders/Clientes%2F2027'],
      ['POST', '/api/v1/webmail/folders/Trash/empty'],
    ]);
    expect(JSON.parse(String(calls[1]?.init.body))).toEqual({ name: 'Clientes/2027' });
  });

  it('un 409 de carpeta protegida llega con su codigo', async () => {
    mockFetch(() => json(409, { error: { code: 'FOLDER_PROTECTED', message: 'protegida' } }));
    await expect(webmailApi.deleteFolder('INBOX')).rejects.toMatchObject({
      status: 409,
      code: 'FOLDER_PROTECTED',
    });
  });

  it('la busqueda avanzada viaja en la query con los nombres del contrato', async () => {
    const calls = mockFetch(() => json(200, { data: [] }));
    await webmailApi.messages('INBOX', {
      from: 'luis',
      since: '2026-09-01',
      before: '2026-09-10',
      unread: true,
      hasAttachments: true,
      flagged: false,
    });
    const url = new URL(calls[0]?.url ?? '', 'http://x');
    expect(Object.fromEntries(url.searchParams)).toEqual({
      from: 'luis',
      since: '2026-09-01',
      before: '2026-09-10',
      unread: 'true',
      has_attachments: 'true',
    });
  });

  it('descarga el original como bytes con el nombre del servicio', async () => {
    const calls = mockFetch(
      () =>
        new Response('From: a@x', {
          status: 200,
          headers: {
            'Content-Type': 'message/rfc822',
            'Content-Disposition': 'attachment; filename="pedido.eml"',
          },
        }),
    );
    const raw = await webmailApi.downloadRaw('INBOX', 5);
    expect(calls[0]?.url).toBe(endpoints.webmail.raw('INBOX', 5));
    expect(raw.filename).toBe('pedido.eml');
    expect(raw.contentType).toBe('message/rfc822');
  });

  it('programa el envio con send_at, la clave de idempotencia y el html', async () => {
    const calls = mockFetch(() =>
      json(202, { data: { scheduled: { id: 's1', send_at: '2026-09-25T08:00:00Z' } } }),
    );
    const ref = await webmailApi.schedule(
      {
        to: ['a@x.com'],
        cc: [],
        bcc: [],
        subject: 'Hola',
        text: '',
        html: '<p>Hola</p>',
        attachments: [],
      },
      '2026-09-25T08:00:00Z',
      { idempotencyKey: 'clave-programada-0001' },
    );
    expect(ref).toEqual({ id: 's1', send_at: '2026-09-25T08:00:00Z' });
    const form = calls[0]?.init.body as FormData;
    expect(form.get('send_at')).toBe('2026-09-25T08:00:00Z');
    expect(form.get('html')).toBe('<p>Hola</p>');
    expect(headersOf(calls[0])['Idempotency-Key']).toBe('clave-programada-0001');
  });

  it('cambia la hora y cancela envios programados', async () => {
    const calls = mockFetch((call) =>
      call.init.method === 'DELETE' ? new Response(null, { status: 204 }) : json(200, { data: {} }),
    );
    await webmailApi.reschedule('s1', '2026-09-26T08:00:00Z');
    await webmailApi.cancelScheduled('s1');
    expect(calls.map((c) => c.init.method)).toEqual(['PATCH', 'DELETE']);
    expect(calls[0]?.url).toBe(endpoints.webmail.scheduledItem('s1'));
  });

  it('guarda un contacto con If-Match y un 412 llega como PRECONDITION_FAILED', async () => {
    const calls = mockFetch(() =>
      json(412, { error: { code: 'PRECONDITION_FAILED', message: 'cambio' } }),
    );
    const input = {
      name: 'Luis',
      given_name: '',
      family_name: '',
      emails: [],
      phones: [],
      organization: '',
      title: '',
      notes: '',
      birthday: '',
    };
    await expect(webmailApi.updateContact('c1', input, '"etag-1"')).rejects.toMatchObject({
      status: 412,
      code: ERROR_CODES.PRECONDITION_FAILED,
    });
    expect(calls[0]?.init.method).toBe('PUT');
    expect(headersOf(calls[0])['If-Match']).toBe('"etag-1"');
  });

  it('importa vCard como multipart en el campo file', async () => {
    const calls = mockFetch(() => json(200, { data: { imported: 2, updated: 0, skipped: [] } }));
    const file = new File(['BEGIN:VCARD'], 'agenda.vcf', { type: 'text/vcard' });
    expect(await webmailApi.importContacts(file)).toEqual({ imported: 2, updated: 0, skipped: [] });
    expect((calls[0]?.init.body as FormData).get('file')).toBeInstanceOf(File);
  });

  it('pide las ocurrencias de la ventana visible y una respuesta vacia es una lista', async () => {
    const calls = mockFetch(() => json(200, { data: null }));
    const list = await webmailApi.calendarOccurrences(
      '2026-09-01T00:00:00.000Z',
      '2026-10-13T00:00:00.000Z',
    );
    expect(list).toEqual([]);
    const url = new URL(calls[0]?.url ?? '', 'http://x');
    expect(url.pathname).toBe(endpoints.webmail.calendarEvents);
    expect(url.searchParams.get('start')).toBe('2026-09-01T00:00:00.000Z');
  });

  it('planificacion: invitaciones sin notificar, apariciones, disponibilidad y respuestas', async () => {
    const calls = mockFetch((call) => {
      if (call.init.method === 'DELETE' && call.url.includes('/occurrences/')) {
        return json(200, { data: { id: 'e1', invitations: null } });
      }
      if (call.init.method === 'DELETE') return new Response(null, { status: 204 });
      return json(200, { data: [] });
    });
    expect(await webmailApi.deleteCalendarEvent('e1', false)).toBeNull();
    expect(new URL(calls[0]?.url ?? '', 'http://x').searchParams.get('notify')).toBe('false');
    await webmailApi.deleteCalendarOccurrence('e1', '2026-10-02T15:00:00Z', '"v1"');
    expect(new URL(calls[1]?.url ?? '', 'http://x').pathname).toBe(
      endpoints.webmail.calendarOccurrence('e1', '2026-10-02T15:00:00Z'),
    );
    expect(new URL(calls[1]?.url ?? '', 'http://x').searchParams.get('notify')).toBeNull();
    expect((calls[1]?.init.headers as Record<string, string>)['If-Match']).toBe('"v1"');
    await webmailApi.availability(
      ['a@x.pe', 'b@x.pe'],
      '2026-10-01T00:00:00Z',
      '2026-10-02T00:00:00Z',
    );
    const availability = new URL(calls[2]?.url ?? '', 'http://x');
    expect(availability.pathname).toBe(endpoints.webmail.availability);
    expect(availability.searchParams.get('addresses')).toBe('a@x.pe,b@x.pe');
    await webmailApi.respondInvitation('Clientes/2026', 7, 'TENTATIVE');
    expect(calls[3]?.url).toContain('/webmail/invitations/Clientes%2F2026/7/respond');
    expect(JSON.parse(String(calls[3]?.init.body))).toEqual({ response: 'TENTATIVE' });
  });

  it('cambia la contrasena con la actual y la nueva; 204 sin cuerpo', async () => {
    const calls = mockFetch(() => new Response(null, { status: 204 }));
    await webmailApi.changePassword('vieja', 'nueva-segura');
    expect(JSON.parse(String(calls[0]?.init.body))).toEqual({
      current_password: 'vieja',
      new_password: 'nueva-segura',
    });
  });
});
