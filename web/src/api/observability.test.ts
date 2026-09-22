import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { setAccessToken } from './client';
import { endpoints } from './endpoints';
import { observabilityApi } from './observability';

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
      return new Response(JSON.stringify(body), {
        status,
        headers: { 'Content-Type': 'application/json' },
      });
    }),
  );
  return calls;
}

describe('visor de registros (API de observability)', () => {
  beforeEach(() => setAccessToken(null));
  afterEach(() => {
    vi.unstubAllGlobals();
    setAccessToken(null);
  });

  it('la lista de servicios llega del API sin el sobre', async () => {
    const calls = mockFetch(200, { data: { services: ['gateway', 'postfix-mail'] } });
    expect(await observabilityApi.listLogServices()).toEqual(['gateway', 'postfix-mail']);
    expect(calls[0]).toEqual({ method: 'GET', url: endpoints.observability.logServices });
  });

  it('una consulta manda cada parametro en la URL, con el texto escapado y sin los vacios', async () => {
    const page = { service: 'postfix-mail', entries: [] };
    const calls = mockFetch(200, { data: page });
    expect(
      await observabilityApi.queryLogs({
        service: 'postfix-mail',
        q: 'a"b} |= "c&d',
        since: '2026-09-21T11:00:00.000Z',
        limit: 200,
        direction: 'forward',
      }),
    ).toEqual(page);
    const url = new URL(calls[0]?.url ?? '', 'http://localhost');
    expect(url.pathname).toBe(endpoints.observability.logs);
    expect(url.searchParams.get('service')).toBe('postfix-mail');
    expect(url.searchParams.get('q')).toBe('a"b} |= "c&d');
    expect(url.searchParams.get('since')).toBe('2026-09-21T11:00:00.000Z');
    expect(url.searchParams.get('limit')).toBe('200');
    expect(url.searchParams.get('direction')).toBe('forward');
    expect(url.searchParams.has('until')).toBe(false);
  });
});
