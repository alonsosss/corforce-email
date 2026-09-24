/*
 * Service worker de Core Force Mail: hace la aplicacion instalable y guarda SOLO la carcasa
 * estatica (los ficheros con hash de contenido que genera Vite, los iconos, el manifiesto y la
 * pagina sin conexion). Nunca guarda ni sirve nada de /api, ni el documento de la aplicacion: el
 * gateway lo sirve en cada visita con un nonce nuevo en la CSP, y el correo y la sesion no pasan por
 * aqui. Sin conexion, una navegacion recibe la pagina estatica sin conexion.
 */
const CACHE = 'cf-shell-v1';
const OFFLINE_URL = '/offline.html';
const STATIC_FILES = [
  OFFLINE_URL,
  '/manifest.webmanifest',
  '/favicon.svg',
  '/icons/icon.svg',
  '/icons/icon-192.png',
  '/icons/icon-512.png',
];
// Mismo patron que nginx.conf usa para servir como inmutables los ficheros con hash.
const HASHED = /^\/assets\/[^/]+-[A-Za-z0-9_-]{8,}\.(?:js|css|woff2?|svg|png|jpe?g|webp)$/;
const MAX_ENTRIES = 300;

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches
      .open(CACHE)
      .then((cache) => cache.addAll(STATIC_FILES))
      .then(() => self.skipWaiting()),
  );
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

function cacheable(response) {
  return response.ok && response.type === 'basic';
}

// La cache no crece sin limite: los ficheros con hash de versiones viejas salen primero.
async function trim(cache) {
  const keys = await cache.keys();
  for (let i = 0; i < keys.length - MAX_ENTRIES; i += 1) {
    await cache.delete(keys[i]);
  }
}

// Un fichero con hash no cambia nunca: si esta guardado, se sirve sin red.
async function immutable(request) {
  const cache = await caches.open(CACHE);
  const hit = await cache.match(request);
  if (hit) return hit;
  const response = await fetch(request);
  if (cacheable(response)) {
    await cache.put(request, response.clone());
    await trim(cache);
  }
  return response;
}

// Iconos, manifiesto y pagina sin conexion: se sirven guardados y se renuevan por detras.
async function revalidated(request) {
  const cache = await caches.open(CACHE);
  const hit = await cache.match(request);
  const refresh = fetch(request)
    .then(async (response) => {
      if (cacheable(response)) await cache.put(request, response.clone());
      return response;
    })
    .catch(() => null);
  if (hit) return hit;
  return (await refresh) ?? Response.error();
}

async function navigate(request) {
  try {
    return await fetch(request);
  } catch {
    return (await caches.match(OFFLINE_URL)) ?? Response.error();
  }
}

self.addEventListener('fetch', (event) => {
  const { request } = event;
  if (request.method !== 'GET') return;
  const url = new URL(request.url);
  if (url.origin !== self.location.origin || url.pathname.startsWith('/api/')) return;
  if (request.mode === 'navigate') {
    event.respondWith(navigate(request));
    return;
  }
  if (HASHED.test(url.pathname)) {
    event.respondWith(immutable(request));
    return;
  }
  if (STATIC_FILES.includes(url.pathname) && url.search === '') {
    event.respondWith(revalidated(request));
  }
});
