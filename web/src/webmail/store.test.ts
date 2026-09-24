import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, ERROR_CODES } from '@/api/errors';
import { webmailApi, type WebmailSession } from '@/api/webmail';
import { resetWebmailCatalogs, senderIdentities } from './catalogs';
import { useWebmailStore } from './store';

const SESSION: WebmailSession = {
  username: 'ana@empresa.com',
  display_name: 'Ana',
  expires_at: '2026-09-13T20:00:00Z',
  idle_timeout_seconds: 1800,
  quota: { used_bytes: 10, limit_bytes: 100 },
};

const expired = () =>
  new ApiError(401, { code: ERROR_CODES.SESSION_EXPIRED, message: 'sesion invalida o caducada' });

describe('sesion del webmail', () => {
  beforeEach(() => {
    resetWebmailCatalogs();
    useWebmailStore.setState({
      status: 'anonymous',
      session: null,
      expired: false,
      passwordChanged: false,
      checkError: null,
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('un SESSION_EXPIRED con la sesion abierta vuelve al inicio del buzon con aviso', async () => {
    useWebmailStore.setState({ status: 'authenticated', session: SESSION });
    vi.stubGlobal(
      'fetch',
      vi.fn(
        async () =>
          new Response(JSON.stringify({ error: { code: 'SESSION_EXPIRED', message: 'x' } }), {
            status: 401,
          }),
      ),
    );

    await webmailApi.folders().catch(() => undefined);

    const state = useWebmailStore.getState();
    expect(state.status).toBe('anonymous');
    expect(state.expired).toBe(true);
    expect(state.session).toBeNull();
  });

  it('sin cookie la comprobacion deja la sesion anonima, sin aviso de caducidad', async () => {
    vi.spyOn(webmailApi, 'session').mockRejectedValue(expired());
    await useWebmailStore.getState().check();
    expect(useWebmailStore.getState()).toMatchObject({ status: 'anonymous', expired: false });
  });

  it('si el servicio no responde no se da la sesion por cerrada', async () => {
    vi.spyOn(webmailApi, 'session').mockRejectedValue(
      new ApiError(503, { code: ERROR_CODES.SERVICE_UNAVAILABLE, message: '' }),
    );
    await useWebmailStore.getState().check();
    expect(useWebmailStore.getState().status).toBe('unavailable');
  });

  it('el inicio de sesion solo guarda en memoria lo que devuelve el servicio', async () => {
    const setItem = vi.spyOn(Storage.prototype, 'setItem');
    vi.spyOn(webmailApi, 'login').mockResolvedValue({ ...SESSION, quota: null });
    vi.spyOn(webmailApi, 'session').mockResolvedValue(SESSION);

    await useWebmailStore.getState().login('ana@empresa.com', 'secreta');

    await vi.waitFor(() =>
      expect(useWebmailStore.getState().session?.quota).toEqual(SESSION.quota),
    );
    expect(useWebmailStore.getState().status).toBe('authenticated');
    expect(JSON.stringify(useWebmailStore.getState())).not.toContain('secreta');
    expect(setItem).not.toHaveBeenCalled();
  });

  it('si el servicio no pudo cerrar la sesion, no se finge el cierre', async () => {
    useWebmailStore.setState({ status: 'authenticated', session: SESSION });
    vi.spyOn(webmailApi, 'logout').mockRejectedValue(
      new ApiError(503, { code: ERROR_CODES.SERVICE_UNAVAILABLE, message: '' }),
    );
    await expect(useWebmailStore.getState().logout()).rejects.toBeInstanceOf(ApiError);
    expect(useWebmailStore.getState().status).toBe('authenticated');
  });

  it('al cerrar la sesion se descartan los remitentes y la meta del buzon anterior', async () => {
    useWebmailStore.setState({ status: 'authenticated', session: SESSION });
    const identities = vi
      .spyOn(webmailApi, 'identities')
      .mockResolvedValueOnce([{ email: 'ana@empresa.com', name: 'Ana', primary: true }])
      .mockResolvedValueOnce([{ email: 'luis@empresa.com', name: 'Luis', primary: true }]);
    vi.spyOn(webmailApi, 'logout').mockResolvedValue(null);

    await expect(senderIdentities.get()).resolves.toMatchObject([{ email: 'ana@empresa.com' }]);
    await expect(senderIdentities.get()).resolves.toMatchObject([{ email: 'ana@empresa.com' }]);
    await useWebmailStore.getState().logout();
    await expect(senderIdentities.get()).resolves.toMatchObject([{ email: 'luis@empresa.com' }]);
    expect(identities).toHaveBeenCalledTimes(2);
  });

  it('cerrar una sesion que ya habia caducado cuenta como cerrada', async () => {
    useWebmailStore.setState({ status: 'authenticated', session: SESSION });
    vi.spyOn(webmailApi, 'logout').mockRejectedValue(expired());
    await useWebmailStore.getState().logout();
    expect(useWebmailStore.getState()).toMatchObject({ status: 'anonymous', session: null });
  });
});
