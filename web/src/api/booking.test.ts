import { afterEach, describe, expect, it, vi } from 'vitest';
import { setAccessToken } from './client';
import { bookingApi } from './booking';
import { endpoints } from './endpoints';
import { ApiError } from './errors';

const target = {
  cell: 'pe-01',
  tenant: '11111111-1111-4111-8111-111111111111',
  page: 'enlace-publico-de-prueba-01',
};

describe('cliente de la pagina publica de citas', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    setAccessToken(null);
  });

  it('va sin ninguna credencial ni token y con la ventana en la consulta', async () => {
    setAccessToken('token-de-la-plataforma');
    const fetchMock = vi.fn(
      async () =>
        new Response(JSON.stringify({ data: { title: 'Demo', slots: [] } }), { status: 200 }),
    );
    vi.stubGlobal('fetch', fetchMock);
    await bookingApi.page(target, '2026-10-01T00:00:00Z', '2026-10-08T00:00:00Z');
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    const parsed = new URL(url, 'http://x');
    expect(parsed.pathname).toBe(
      endpoints.publicBooking.page(target.cell, target.tenant, target.page),
    );
    expect(parsed.searchParams.get('start')).toBe('2026-10-01T00:00:00Z');
    expect(init.credentials).toBe('omit');
    expect(JSON.stringify(init.headers)).not.toContain('Authorization');
  });

  it('un rechazo llega como ApiError con su codigo', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(
        async () =>
          new Response(
            JSON.stringify({ error: { code: 'SLOT_UNAVAILABLE', message: 'ocupado' } }),
            {
              status: 409,
            },
          ),
      ),
    );
    const err = await bookingApi
      .book(target, {
        start: '2026-10-01T15:00:00Z',
        name: 'Luis',
        email: 'l@c.pe',
        note: '',
        website: '',
      })
      .catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).code).toBe('SLOT_UNAVAILABLE');
    expect((err as ApiError).status).toBe(409);
  });
});
