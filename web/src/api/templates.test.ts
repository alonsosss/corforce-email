import { afterEach, describe, expect, it, vi } from 'vitest';
import { setAccessToken } from './client';
import { endpoints } from './endpoints';
import { ApiError, ERROR_CODES } from './errors';
import { deliverabilityIssues, templatesApi } from './templates';

interface Call {
  url: string;
  method: string;
  headers: Record<string, string>;
  body: BodyInit | null | undefined;
}

function respond(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function mockFetch(handler: (call: Call) => Response): Call[] {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const call: Call = {
        url: String(input),
        method: init?.method ?? 'GET',
        headers: { ...((init?.headers as Record<string, string> | undefined) ?? {}) },
        body: init?.body,
      };
      calls.push(call);
      return handler(call);
    }),
  );
  return calls;
}

afterEach(() => {
  vi.unstubAllGlobals();
  setAccessToken(null);
});

describe('cliente de plantillas: contratos del editor', () => {
  it('sube la imagen como multipart con el campo file y sin Content-Type propio', async () => {
    const asset = {
      id: 'a1',
      url: 'https://app.example/media/public/t/templates/abc.png',
      content_type: 'image/png',
      size_bytes: 10,
      width: 1,
      height: 1,
      name: 'logo.png',
      created_at: '2026-09-23T10:00:00Z',
    };
    const calls = mockFetch(() => respond(201, { data: asset }));
    const file = new Blob(['png'], { type: 'image/png' });

    await expect(templatesApi.uploadAsset(file, 'logo.png')).resolves.toEqual(asset);

    const call = calls[0]!;
    expect(call.url).toBe(endpoints.templates.assets);
    expect(call.method).toBe('POST');
    expect(call.headers['Content-Type']).toBeUndefined();
    expect(call.body).toBeInstanceOf(FormData);
    const sent = (call.body as FormData).get('file');
    expect(sent).toBeInstanceOf(Blob);
    expect((sent as File).name).toBe('logo.png');
  });

  it('muestra el 503 SCANNER_UNAVAILABLE como error con su codigo', async () => {
    mockFetch(() =>
      respond(503, { error: { code: ERROR_CODES.SCANNER_UNAVAILABLE, message: 'sin clamav' } }),
    );
    const err = await templatesApi.uploadAsset(new Blob(['x']), 'x.png').catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).is(ERROR_CODES.SCANNER_UNAVAILABLE)).toBe(true);
  });

  it('lista imagenes pagina a pagina con next_cursor', async () => {
    const calls = mockFetch((call) =>
      call.url.includes('cursor=c1')
        ? respond(200, { data: { items: null, next_cursor: null } })
        : respond(200, { data: { items: [{ id: 'a1' }], next_cursor: 'c1' } }),
    );
    const first = await templatesApi.listAssets({ limit: 20 });
    expect(first.items.map((a) => a.id)).toEqual(['a1']);
    expect(first.nextCursor).toBe('c1');
    expect(calls[0]!.url).toBe(`${endpoints.templates.assets}?limit=20`);

    const second = await templatesApi.listAssets({ cursor: 'c1' });
    expect(second).toEqual({ items: [], nextCursor: null });
  });

  it('normaliza el kit vacio que devuelve el servicio', async () => {
    mockFetch(() => respond(200, { data: { colors: null, fonts: null, updated_at: null } }));
    await expect(templatesApi.brandKit()).resolves.toEqual({
      logo_asset_id: null,
      colors: [],
      fonts: [],
      image_hosts: [],
      footer: { company: '', address: '', website: '', support_email: '' },
      updated_at: null,
    });
  });

  it('verifica un contenido sin guardarlo', async () => {
    const calls = mockFetch(() =>
      respond(200, {
        data: {
          passed: true,
          issues: null,
          stats: { html_bytes: 1, text_chars: 1, images: 0, links: 1, text_image_ratio: 1 },
          spam: { available: false },
        },
      }),
    );
    const result = await templatesApi.check({
      kind: 'marketing',
      subject: 'Hola',
      html: '<p>x</p>',
    });
    expect(result.issues).toEqual([]);
    expect(result.spam.symbols).toEqual([]);
    expect(calls[0]!.url).toBe(endpoints.templates.check);
    expect(JSON.parse(String(calls[0]!.body))).toEqual({
      kind: 'marketing',
      subject: 'Hola',
      html: '<p>x</p>',
    });
  });
});

describe('deliverabilityIssues', () => {
  const issue = { code: 'missing_unsubscribe', severity: 'error', message: 'Falta la baja' };

  it('lee las issues del 409 DELIVERABILITY_FAILED', () => {
    const err = new ApiError(
      409,
      { code: ERROR_CODES.DELIVERABILITY_FAILED, message: 'no pasa' },
      { error: { code: ERROR_CODES.DELIVERABILITY_FAILED, issues: [issue, { bad: 1 }] } },
    );
    expect(deliverabilityIssues(err)).toEqual([issue]);
  });

  it('null si el error es otro o no trae issues', () => {
    expect(
      deliverabilityIssues(new ApiError(409, { code: ERROR_CODES.CONFLICT, message: '' }, {})),
    ).toBeNull();
    expect(
      deliverabilityIssues(
        new ApiError(409, { code: ERROR_CODES.DELIVERABILITY_FAILED, message: '' }, { error: {} }),
      ),
    ).toBeNull();
  });
});
