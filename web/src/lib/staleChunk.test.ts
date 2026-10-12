import { describe, expect, it, vi } from 'vitest';
import { isChunkLoadError, reloadOnceForStaleChunk } from './staleChunk';

function memoryStorage() {
  const data = new Map<string, string>();
  return {
    getItem: (key: string) => data.get(key) ?? null,
    setItem: (key: string, value: string) => void data.set(key, value),
  };
}

describe('error de carga de modulo', () => {
  it('reconoce los mensajes de Chrome, Firefox y Safari', () => {
    expect(
      isChunkLoadError(
        new TypeError('Failed to fetch dynamically imported module: https://x/assets/A-1.js'),
      ),
    ).toBe(true);
    expect(isChunkLoadError(new Error('error loading dynamically imported module'))).toBe(true);
    expect(isChunkLoadError(new Error('Importing a module script failed.'))).toBe(true);
  });

  it('no confunde otros errores', () => {
    expect(isChunkLoadError(new Error('Cannot read properties of undefined'))).toBe(false);
    expect(isChunkLoadError(null)).toBe(false);
    expect(isChunkLoadError({ message: 'Failed to fetch dynamically imported module' })).toBe(
      false,
    );
  });
});

describe('recarga unica', () => {
  it('recarga la primera vez y no la segunda dentro del minuto', () => {
    const storage = memoryStorage();
    const reload = vi.fn();
    let now = 1_000_000;
    const opts = { storage, reload, now: () => now };

    expect(reloadOnceForStaleChunk(opts)).toBe(true);
    now += 5_000;
    expect(reloadOnceForStaleChunk(opts)).toBe(false);
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it('pasado el minuto vuelve a poder recargar', () => {
    const storage = memoryStorage();
    const reload = vi.fn();
    let now = 1_000_000;
    const opts = { storage, reload, now: () => now };
    reloadOnceForStaleChunk(opts);
    now += 61_000;
    expect(reloadOnceForStaleChunk(opts)).toBe(true);
    expect(reload).toHaveBeenCalledTimes(2);
  });

  it('sin almacenamiento no recarga: evita el bucle', () => {
    const reload = vi.fn();
    const broken = {
      getItem: () => {
        throw new Error('denegado');
      },
      setItem: () => {
        throw new Error('denegado');
      },
    };
    expect(reloadOnceForStaleChunk({ storage: broken, reload })).toBe(false);
    expect(reload).not.toHaveBeenCalled();
  });
});
