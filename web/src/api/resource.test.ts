import { describe, expect, it, vi } from 'vitest';
import { cachedResource, sessionResource } from './resource';

describe('recursos compartidos', () => {
  it('una sola peticion para todas las lecturas; un fallo no se guarda', async () => {
    const load = vi.fn().mockRejectedValueOnce(new Error('caido')).mockResolvedValue('ok');
    const resource = cachedResource(load);
    await expect(resource.get()).rejects.toThrow('caido');
    await expect(Promise.all([resource.get(), resource.get()])).resolves.toEqual(['ok', 'ok']);
    expect(load).toHaveBeenCalledTimes(2);
  });

  it('reset descarta lo de la sesion anterior y la siguiente lectura vuelve a pedir', async () => {
    const load = vi.fn().mockResolvedValueOnce('buzon-a').mockResolvedValueOnce('buzon-b');
    const resource = sessionResource(load);
    await expect(resource.get()).resolves.toBe('buzon-a');
    resource.reset();
    await expect(resource.get()).resolves.toBe('buzon-b');
    expect(load).toHaveBeenCalledTimes(2);
  });

  it('una peticion de antes del reset que falla no borra la de despues', async () => {
    let failOld: (err: Error) => void = () => undefined;
    const load = vi
      .fn()
      .mockReturnValueOnce(
        new Promise<string>((_, reject) => {
          failOld = reject;
        }),
      )
      .mockResolvedValueOnce('nuevo');
    const resource = sessionResource(load);
    const old = resource.get();
    resource.reset();
    const fresh = resource.get();
    failOld(new Error('tarde'));
    await expect(old).rejects.toThrow('tarde');
    await expect(fresh).resolves.toBe('nuevo');
    await expect(resource.get()).resolves.toBe('nuevo');
    expect(load).toHaveBeenCalledTimes(2);
  });
});
