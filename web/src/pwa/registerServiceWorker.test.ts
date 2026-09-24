import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  registerServiceWorker,
  SERVICE_WORKER_URL,
  type ServiceWorkerEnv,
} from './registerServiceWorker';

function env(overrides: Partial<ServiceWorkerEnv> = {}) {
  const register = vi.fn().mockResolvedValue({});
  return {
    env: { production: true, secure: true, container: { register }, ...overrides },
    register,
  };
}

describe('registro del service worker', () => {
  afterEach(() => vi.restoreAllMocks());

  it('solo en produccion, en un contexto seguro y con soporte del navegador', () => {
    for (const overrides of [{ production: false }, { secure: false }, { container: undefined }]) {
      const { env: e, register } = env(overrides);
      expect(registerServiceWorker(e)).toBe(false);
      expect(register).not.toHaveBeenCalled();
    }
  });

  it('registra /sw.js con alcance raiz sin cache HTTP del propio script', () => {
    const { env: e, register } = env();
    expect(registerServiceWorker(e)).toBe(true);
    expect(register).toHaveBeenCalledWith(SERVICE_WORKER_URL, {
      scope: '/',
      updateViaCache: 'none',
    });
  });

  it('un rechazo del navegador o de la CSP solo se avisa en la consola', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined);
    const register = vi.fn().mockRejectedValue(new Error('bloqueado'));
    registerServiceWorker({ production: true, secure: true, container: { register } });
    await vi.waitFor(() => expect(warn).toHaveBeenCalled());
  });
});
