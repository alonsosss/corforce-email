import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { setAccessToken } from './client';
import { endpoints } from './endpoints';
import { mailSecurityApi } from './mailSecurity';

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

describe('antispam (API de mail-security)', () => {
  beforeEach(() => setAccessToken(null));
  afterEach(() => {
    vi.unstubAllGlobals();
    setAccessToken(null);
  });

  it('las estadisticas y el historial van a sus rutas de plataforma', async () => {
    const calls = mockFetch(200, { data: { scanned: 1 } });
    expect(await mailSecurityApi.rspamdStats()).toEqual({ scanned: 1 });
    await mailSecurityApi.rspamdHistory(200);
    expect(calls.map((c) => `${c.method} ${c.url}`)).toEqual([
      `GET ${endpoints.mailSecurity.rspamdStats}`,
      `GET ${endpoints.mailSecurity.rspamdHistory}?limit=200`,
    ]);
  });
});
