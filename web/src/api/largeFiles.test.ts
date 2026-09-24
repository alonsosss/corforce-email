import { afterEach, describe, expect, it, vi } from 'vitest';
import { endpoints } from './endpoints';
import type { ApiError } from './errors';
import { largeFilesApi } from './largeFiles';

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

describe('api de ficheros grandes', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('sube el fichero en multipart con la sesion del buzon y las opciones', async () => {
    const calls = mockFetch(() => json(201, { data: { id: 'f1', name: 'a.pdf' } }));
    const file = new File(['contenido'], 'a.pdf', { type: 'application/pdf' });
    const shared = await largeFilesApi.upload(file, { expiresInDays: 3, maxDownloads: 4 });
    expect(shared.id).toBe('f1');
    const [call] = calls;
    expect(call?.init.method).toBe('POST');
    expect(call?.init.credentials).toBe('include');
    expect(call?.url).toContain(endpoints.webmail.largeFiles);
    expect(call?.url).toContain('expires_in_days=3');
    expect(call?.url).toContain('max_downloads=4');
    const body = call?.init.body as FormData;
    expect((body.get('file') as File).name).toBe('a.pdf');
  });

  it('sin opciones no manda parametros', async () => {
    const calls = mockFetch(() => json(201, { data: { id: 'f1' } }));
    await largeFilesApi.upload(new File(['x'], 'x.bin'));
    expect(calls[0]?.url).not.toContain('?');
  });

  it('revoca por id y entrega los rechazos con su codigo', async () => {
    const calls = mockFetch(() =>
      json(422, { error: { code: 'FILE_NOT_ACTIVE', message: 'ya no vigente' } }),
    );
    await expect(largeFilesApi.revoke('f/1')).rejects.toMatchObject({
      code: 'FILE_NOT_ACTIVE',
    } satisfies Partial<ApiError>);
    expect(calls[0]?.init.method).toBe('DELETE');
    expect(calls[0]?.url).toContain(`${endpoints.webmail.largeFiles}/f%2F1`);
  });
});
