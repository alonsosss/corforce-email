import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { setAccessToken } from './client';
import { endpoints } from './endpoints';
import { mailSecurityApi, QUEUE_MAX_LIMIT } from './mailSecurity';

interface RecordedCall {
  url: string;
  method: string;
}

function mockFetch(status: number, body: unknown): RecordedCall[] {
  const calls: RecordedCall[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({ url: String(input), method: init?.method ?? 'GET' });
      return status === 204
        ? new Response(null, { status })
        : new Response(JSON.stringify(body), {
            status,
            headers: { 'Content-Type': 'application/json' },
          });
    }),
  );
  return calls;
}

describe('cola de correo (API de mail-security)', () => {
  beforeEach(() => setAccessToken(null));
  afterEach(() => {
    vi.unstubAllGlobals();
    setAccessToken(null);
  });

  it('lista con el limite pedido y devuelve la cola sin el sobre', async () => {
    const listing = { total: 1, truncated: false, items: [{ queue_id: 'ABCDEF1234' }] };
    const calls = mockFetch(200, { data: listing });

    const got = await mailSecurityApi.listQueue(QUEUE_MAX_LIMIT);

    expect(got).toEqual(listing);
    expect(calls[0]?.method).toBe('GET');
    expect(calls[0]?.url).toBe(`${endpoints.mailSecurity.queue}?limit=${QUEUE_MAX_LIMIT}`);
  });

  it('cada accion va a su ruta y borrar es un DELETE sobre el mensaje', async () => {
    const calls = mockFetch(204, null);

    await mailSecurityApi.queueAction('ABCDEF1234', 'retry');
    await mailSecurityApi.queueAction('ABCDEF1234', 'hold');
    await mailSecurityApi.queueAction('ABCDEF1234', 'unhold');
    await mailSecurityApi.deleteQueueMessage('ABCDEF1234');

    expect(calls.map((c) => `${c.method} ${c.url}`)).toEqual([
      `POST ${endpoints.mailSecurity.queue}/ABCDEF1234/retry`,
      `POST ${endpoints.mailSecurity.queue}/ABCDEF1234/hold`,
      `POST ${endpoints.mailSecurity.queue}/ABCDEF1234/unhold`,
      `DELETE ${endpoints.mailSecurity.queue}/ABCDEF1234`,
    ]);
  });

  it('un identificador con caracteres de ruta no cambia la ruta', async () => {
    const calls = mockFetch(204, null);
    await mailSecurityApi.deleteQueueMessage('../flush');
    expect(calls[0]?.url).toBe(`${endpoints.mailSecurity.queue}/..%2Fflush`);
  });

  it('vaciar la cola es un POST a /queue/flush', async () => {
    const calls = mockFetch(202, { data: { status: 'queued' } });
    await mailSecurityApi.flushQueue();
    expect(`${calls[0]?.method} ${calls[0]?.url}`).toBe(
      `POST ${endpoints.mailSecurity.queueFlush}`,
    );
  });
});
