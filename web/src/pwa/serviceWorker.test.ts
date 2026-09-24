import { beforeEach, describe, expect, it, vi } from 'vitest';
import source from '../../public/sw.js?raw';

/*
 * El service worker se ejecuta aqui sobre un entorno falso (self, caches, fetch) para comprobar su
 * contrato: solo la carcasa estatica, nunca /api ni el documento.
 */

const ORIGIN = 'https://mail.test';

interface FakeRequest {
  method: string;
  url: string;
  mode: string;
}

type Handler = (event: unknown) => void;

function basic(body: string, status = 200): Response {
  const res = new Response(body, { status });
  Object.defineProperty(res, 'type', { value: 'basic' });
  return res;
}

function keyOf(req: FakeRequest | string): string {
  return new URL(typeof req === 'string' ? req : req.url, ORIGIN).toString();
}

function setup() {
  const handlers: Record<string, Handler> = {};
  const store = new Map<string, Response>();
  const cache = {
    match: async (req: FakeRequest | string) => store.get(keyOf(req))?.clone(),
    put: async (req: FakeRequest | string, res: Response) => void store.set(keyOf(req), res),
    keys: async () => [...store.keys()].map((url) => ({ url })),
    delete: async (req: FakeRequest | string) => store.delete(keyOf(req)),
    addAll: async (urls: string[]) => {
      for (const url of urls) store.set(keyOf(url), basic(`precache ${url}`));
    },
  };
  const deleted: string[] = [];
  const caches = {
    open: async () => cache,
    match: async (req: FakeRequest | string) => cache.match(req),
    keys: async () => ['cf-shell-v1', 'cf-shell-v0'],
    delete: async (name: string) => {
      deleted.push(name);
      return true;
    },
  };
  const fetchMock = vi.fn(async (req: FakeRequest) => basic(`red ${req.url}`));
  const self = {
    location: new URL(`${ORIGIN}/`),
    addEventListener: (type: string, fn: Handler) => {
      handlers[type] = fn;
    },
    skipWaiting: vi.fn(async () => undefined),
    clients: { claim: vi.fn(async () => undefined) },
  };
  new Function('self', 'caches', 'fetch', 'Response', source)(self, caches, fetchMock, Response);

  async function lifecycle(type: 'install' | 'activate') {
    let pending: Promise<unknown> = Promise.resolve();
    handlers[type]?.({ waitUntil: (p: Promise<unknown>) => (pending = p) });
    await pending;
  }

  async function request(path: string, init: Partial<FakeRequest> = {}) {
    const req: FakeRequest = {
      method: 'GET',
      url: new URL(path, ORIGIN).toString(),
      mode: 'cors',
      ...init,
    };
    let responded: Promise<Response> | null = null;
    handlers.fetch?.({ request: req, respondWith: (p: Promise<Response>) => (responded = p) });
    return responded ? await (responded as Promise<Response>) : null;
  }

  return { store, fetchMock, deleted, lifecycle, request, self };
}

describe('service worker: solo la carcasa estatica', () => {
  let sw: ReturnType<typeof setup>;
  beforeEach(async () => {
    sw = setup();
    await sw.lifecycle('install');
  });

  it('al instalarse guarda la pagina sin conexion y los iconos, y al activarse borra las caches viejas', async () => {
    expect(sw.store.has(`${ORIGIN}/offline.html`)).toBe(true);
    expect(sw.store.has(`${ORIGIN}/icons/icon-192.png`)).toBe(true);
    await sw.lifecycle('activate');
    expect(sw.deleted).toEqual(['cf-shell-v0']);
  });

  it('nunca intercepta el API, las escrituras ni otros origenes', async () => {
    expect(await sw.request('/api/v1/webmail/folders')).toBeNull();
    expect(await sw.request('/api/v1/webmail/session', { mode: 'navigate' })).toBeNull();
    expect(await sw.request('/assets/index-AbCd1234.js', { method: 'POST' })).toBeNull();
    expect(await sw.request('https://otro.test/assets/index-AbCd1234.js')).toBeNull();
    expect(await sw.request('/version.json')).toBeNull();
    expect([...sw.store.keys()].some((k) => k.includes('/api/'))).toBe(false);
  });

  it('el documento siempre va a la red y nunca se guarda; sin red, la pagina sin conexion', async () => {
    const online = await sw.request('/webmail?folder=INBOX', { mode: 'navigate' });
    expect(await online?.text()).toContain('red');
    expect(sw.store.has(`${ORIGIN}/webmail?folder=INBOX`)).toBe(false);
    sw.fetchMock.mockRejectedValueOnce(new TypeError('sin red'));
    const offline = await sw.request('/webmail', { mode: 'navigate' });
    expect(await offline?.text()).toContain('precache /offline.html');
  });

  it('un fichero con hash se guarda la primera vez y despues se sirve sin red', async () => {
    const first = await sw.request('/assets/index-AbCd1234.js');
    expect(await first?.text()).toContain('red');
    const calls = sw.fetchMock.mock.calls.length;
    const second = await sw.request('/assets/index-AbCd1234.js');
    expect(await second?.text()).toContain('red');
    expect(sw.fetchMock.mock.calls.length).toBe(calls);
  });

  it('un fichero sin hash no se guarda', async () => {
    expect(await sw.request('/assets/logo.png')).toBeNull();
    expect(await sw.request('/src/main.tsx')).toBeNull();
  });
});
