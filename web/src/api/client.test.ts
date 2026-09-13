import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  api,
  clearStepUpToken,
  getAccessToken,
  onUnauthorized,
  registerStepUpHandler,
  setAccessToken,
} from './client';
import { endpoints } from './endpoints';
import { ApiError, ERROR_CODES } from './errors';

function base64Url(value: object): string {
  return btoa(JSON.stringify(value)).replace(/=+$/, '').replace(/\+/g, '-').replace(/\//g, '_');
}

function makeToken(uid: string, expiresInSeconds: number): string {
  const exp = Math.floor(Date.now() / 1000) + expiresInSeconds;
  return `${base64Url({ alg: 'HS256', typ: 'JWT' })}.${base64Url({ uid, exp })}.firma`;
}

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

interface RecordedCall {
  url: string;
  method: string;
  headers: Record<string, string>;
  body: string | undefined;
  credentials: RequestCredentials | undefined;
}

function mockFetch(handler: (call: RecordedCall) => Response | Promise<Response>): RecordedCall[] {
  const calls: RecordedCall[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const call: RecordedCall = {
        url: String(input),
        method: init?.method ?? 'GET',
        // Copia en el momento de la llamada: el cliente reutiliza el objeto de cabeceras
        // entre reintentos, igual que haria un fetch real al serializarlo.
        headers: { ...((init?.headers as Record<string, string> | undefined) ?? {}) },
        body: typeof init?.body === 'string' ? init.body : undefined,
        credentials: init?.credentials,
      };
      calls.push(call);
      return handler(call);
    }),
  );
  return calls;
}

const BUSINESS = endpoints.users.collection;

describe('cliente HTTP', () => {
  beforeEach(() => {
    setAccessToken(null);
    clearStepUpToken();
    registerStepUpHandler(null);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('renueva una sola vez aunque varias peticiones encuentren el token por vencer', async () => {
    const fresh = makeToken('u1', 300);
    setAccessToken(makeToken('u1', 10));
    const calls = mockFetch(async (call) => {
      if (call.url === endpoints.auth.refresh) {
        await new Promise((resolve) => setTimeout(resolve, 10));
        return jsonResponse(200, { data: { access_token: fresh } });
      }
      return jsonResponse(200, { data: [] });
    });

    await Promise.all([api.get(BUSINESS), api.get(BUSINESS), api.get(BUSINESS)]);

    const refreshes = calls.filter((c) => c.url === endpoints.auth.refresh);
    expect(refreshes).toHaveLength(1);
    expect(refreshes[0]?.credentials).toBe('same-origin');
    expect(JSON.parse(refreshes[0]?.body ?? '{}')).toEqual({ cookie_auth: true });

    const business = calls.filter((c) => c.url === BUSINESS);
    expect(business).toHaveLength(3);
    for (const call of business) {
      expect(call.headers.Authorization).toBe(`Bearer ${fresh}`);
      expect(call.credentials).toBe('same-origin');
    }
    expect(getAccessToken()).toBe(fresh);
  });

  it('ante un 401 renueva y reintenta la peticion una sola vez', async () => {
    const old = makeToken('u1', 3600);
    const fresh = makeToken('u1', 3600);
    setAccessToken(old);
    let businessCalls = 0;
    const calls = mockFetch((call) => {
      if (call.url === endpoints.auth.refresh) {
        return jsonResponse(200, { data: { access_token: fresh } });
      }
      businessCalls += 1;
      return businessCalls === 1
        ? jsonResponse(401, { error: { code: ERROR_CODES.UNAUTHORIZED, message: 'vencido' } })
        : jsonResponse(200, { data: [{ id: 'x' }], meta: { page: 1, total: 1 } });
    });

    const res = await api.get<{ id: string }[]>(BUSINESS);

    expect(res.data).toEqual([{ id: 'x' }]);
    expect(res.meta).toEqual({ page: 1, total: 1 });
    const business = calls.filter((c) => c.url === BUSINESS);
    expect(business).toHaveLength(2);
    expect(business[0]?.headers.Authorization).toBe(`Bearer ${old}`);
    expect(business[1]?.headers.Authorization).toBe(`Bearer ${fresh}`);
    expect(calls.filter((c) => c.url === endpoints.auth.refresh)).toHaveLength(1);
  });

  it('si la renovacion tambien falla, cierra la sesion y avisa una vez', async () => {
    setAccessToken(makeToken('u1', 3600));
    const listener = vi.fn();
    const unsubscribe = onUnauthorized(listener);
    mockFetch(() =>
      jsonResponse(401, { error: { code: ERROR_CODES.UNAUTHORIZED, message: 'no' } }),
    );

    const error = await api.get(BUSINESS).catch((err: unknown) => err);

    expect(error).toBeInstanceOf(ApiError);
    expect((error as ApiError).status).toBe(401);
    expect(getAccessToken()).toBeNull();
    expect(listener).toHaveBeenCalledTimes(1);
    unsubscribe();
  });

  it('pide step-up ante STEP_UP_REQUIRED, reintenta con X-Step-Up y reutiliza el token', async () => {
    setAccessToken(makeToken('u1', 3600));
    const handler = vi.fn(async () => ({ token: 'step-up-token', expiresIn: 300 }));
    registerStepUpHandler(handler);
    const target = endpoints.users.byId('u2');
    const calls = mockFetch((call) =>
      call.headers['X-Step-Up'] === 'step-up-token'
        ? new Response(null, { status: 204 })
        : jsonResponse(403, {
            error: { code: ERROR_CODES.STEP_UP_REQUIRED, message: 'reconfirma' },
          }),
    );

    await api.delete(target);
    await api.delete(target);

    expect(handler).toHaveBeenCalledTimes(1);
    const withStepUp = calls.filter((c) => c.headers['X-Step-Up'] === 'step-up-token');
    expect(withStepUp).toHaveLength(2);
    expect(withStepUp.every((c) => c.method === 'DELETE')).toBe(true);
  });

  it('si el usuario cancela el step-up, la peticion falla con STEP_UP_REQUIRED', async () => {
    setAccessToken(makeToken('u1', 3600));
    registerStepUpHandler(async () => null);
    const calls = mockFetch(() =>
      jsonResponse(403, { error: { code: ERROR_CODES.STEP_UP_REQUIRED, message: 'reconfirma' } }),
    );

    const error = await api.delete(endpoints.users.byId('u2')).catch((err: unknown) => err);

    expect(error).toBeInstanceOf(ApiError);
    expect((error as ApiError).code).toBe(ERROR_CODES.STEP_UP_REQUIRED);
    expect(calls).toHaveLength(1);
  });

  it('convierte una respuesta no JSON en un error con estado y codigo propios', async () => {
    setAccessToken(makeToken('u1', 3600));
    mockFetch(() => new Response('<html>Bad Gateway</html>', { status: 502 }));

    const error = await api.get(BUSINESS).catch((err: unknown) => err);

    expect(error).toBeInstanceOf(ApiError);
    expect((error as ApiError).status).toBe(502);
    expect((error as ApiError).code).toBe(ERROR_CODES.INVALID_RESPONSE);
  });
});
