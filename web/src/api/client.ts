import { apiBase, endpoints } from './endpoints';
import { ApiError, ERROR_CODES } from './errors';
import type { ApiResponse, Envelope } from './types';
import { isTokenExpiring } from '@/lib/jwt';

/*
 * Sesion en el navegador (docs/arquitectura/CSP-Y-SESION.md):
 *
 * - El refresh token viaja en la cookie HttpOnly `cf_rt` (Path=/api/v1/auth). Este codigo
 *   no puede leerla; solo la envia el navegador al renovar.
 * - El access token vive SOLO en memoria, en una propiedad no enumerable de window. Nunca
 *   en localStorage ni sessionStorage. Tras recargar, restoreSession() pide uno nuevo con
 *   la cookie.
 * - Renovacion de vuelo unico 60 s antes de vencer; un 401 provoca UNA renovacion y un
 *   reintento; un 403 STEP_UP_REQUIRED abre el modal de step-up y reintenta con X-Step-Up.
 */

const REFRESH_MARGIN_SECONDS = 60;
const KEEPALIVE_INTERVAL_MS = 30_000;
const STEP_UP_MARGIN_MS = 5_000;
const SESSION_PROP = '__cfSession';

export interface StepUpGrant {
  token: string;
  expiresIn: number;
}

export type StepUpHandler = () => Promise<StepUpGrant | null>;
export type UnauthorizedListener = () => void;

interface SessionHolder {
  token: string | null;
  stepUpToken: string | null;
  stepUpExpiresAt: number;
  stepUpHandler: StepUpHandler | null;
  stepUpInFlight: Promise<string | null> | null;
  refreshing: Promise<string | null> | null;
  unauthorizedListeners: Set<UnauthorizedListener>;
  keepAlive: ReturnType<typeof setInterval> | null;
}

function newHolder(): SessionHolder {
  return {
    token: null,
    stepUpToken: null,
    stepUpExpiresAt: 0,
    stepUpHandler: null,
    stepUpInFlight: null,
    refreshing: null,
    unauthorizedListeners: new Set(),
    keepAlive: null,
  };
}

let fallbackHolder: SessionHolder | null = null;

function session(): SessionHolder {
  if (typeof window === 'undefined') {
    fallbackHolder ??= newHolder();
    return fallbackHolder;
  }
  const w = window as unknown as Record<string, SessionHolder | undefined>;
  if (!w[SESSION_PROP]) {
    Object.defineProperty(window, SESSION_PROP, {
      value: newHolder(),
      enumerable: false,
      writable: false,
      configurable: false,
    });
  }
  return w[SESSION_PROP] as SessionHolder;
}

export function setAccessToken(token: string | null): void {
  session().token = token;
}

export function getAccessToken(): string | null {
  return session().token;
}

export function onUnauthorized(listener: UnauthorizedListener): () => void {
  const s = session();
  s.unauthorizedListeners.add(listener);
  return () => s.unauthorizedListeners.delete(listener);
}

export function registerStepUpHandler(handler: StepUpHandler | null): void {
  session().stepUpHandler = handler;
}

export function clearStepUpToken(): void {
  const s = session();
  s.stepUpToken = null;
  s.stepUpExpiresAt = 0;
}

/** Cierra la sesion en memoria y avisa una sola vez a quien escuche. */
export function dropSession(): void {
  const s = session();
  const hadSession = s.token !== null;
  s.token = null;
  clearStepUpToken();
  if (hadSession) {
    s.unauthorizedListeners.forEach((listener) => listener());
  }
}

export type QueryParams = Record<string, string | number | boolean | undefined | null>;

export interface RequestOptions {
  params?: QueryParams;
  body?: unknown;
  signal?: AbortSignal;
}

type Method = 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';

function buildUrl(path: string, params?: QueryParams): string {
  const query = new URLSearchParams();
  if (params) {
    for (const [key, value] of Object.entries(params)) {
      if (value === undefined || value === null || value === '') continue;
      query.set(key, String(value));
    }
  }
  const qs = query.toString();
  return `${apiBase()}${path}${qs ? `?${qs}` : ''}`;
}

async function refreshAccessToken(): Promise<string | null> {
  try {
    const res = await fetch(buildUrl(endpoints.auth.refresh), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      body: JSON.stringify({ cookie_auth: true }),
      credentials: 'same-origin',
    });
    if (!res.ok) return null;
    const json = (await res.json()) as Envelope<{ access_token?: string }>;
    const token = json.data?.access_token ?? null;
    if (token) session().token = token;
    return token;
  } catch {
    return null;
  }
}

/** Una sola renovacion en vuelo por pestana: las peticiones concurrentes la comparten. */
function refreshSingleFlight(): Promise<string | null> {
  const s = session();
  if (!s.refreshing) {
    s.refreshing = refreshAccessToken().finally(() => {
      s.refreshing = null;
    });
  }
  return s.refreshing;
}

/**
 * Recupera la sesion tras recargar: la unica prueba de que existe es la cookie HttpOnly,
 * y solo el servidor puede leerla.
 */
export async function restoreSession(): Promise<string | null> {
  const current = session().token;
  if (current && !isTokenExpiring(current, REFRESH_MARGIN_SECONDS)) return current;
  return refreshSingleFlight();
}

async function getValidToken(): Promise<string | null> {
  const current = session().token;
  if (!current) return null;
  if (!isTokenExpiring(current, REFRESH_MARGIN_SECONDS)) return current;
  const fresh = await refreshSingleFlight();
  if (!fresh) dropSession();
  return fresh;
}

/** Renovacion proactiva mientras la pestana esta abierta, aunque no haya peticiones. */
export function startSessionKeepAlive(): void {
  const s = session();
  if (s.keepAlive) return;
  s.keepAlive = setInterval(() => {
    void getValidToken();
  }, KEEPALIVE_INTERVAL_MS);
}

export function stopSessionKeepAlive(): void {
  const s = session();
  if (s.keepAlive) {
    clearInterval(s.keepAlive);
    s.keepAlive = null;
  }
}

async function ensureStepUpToken(): Promise<string | null> {
  const s = session();
  if (s.stepUpToken && Date.now() < s.stepUpExpiresAt - STEP_UP_MARGIN_MS) {
    return s.stepUpToken;
  }
  if (!s.stepUpHandler) return null;
  if (!s.stepUpInFlight) {
    const handler = s.stepUpHandler;
    s.stepUpInFlight = handler()
      .then((grant) => {
        if (!grant) return null;
        s.stepUpToken = grant.token;
        s.stepUpExpiresAt = Date.now() + grant.expiresIn * 1000;
        return grant.token;
      })
      .finally(() => {
        s.stepUpInFlight = null;
      });
  }
  return s.stepUpInFlight;
}

interface Exchange<T> {
  res: Response;
  json: Envelope<T> | null;
}

async function exchange<T>(
  method: Method,
  url: string,
  headers: Record<string, string>,
  body: string | undefined,
  signal?: AbortSignal,
): Promise<Exchange<T>> {
  let res: Response;
  try {
    res = await fetch(url, { method, headers, body, signal, credentials: 'same-origin' });
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') throw err;
    throw new ApiError(0, { code: ERROR_CODES.NETWORK_ERROR, message: '' });
  }
  if (res.status === 204) return { res, json: null };
  const raw = await res.text();
  if (!raw) return { res, json: null };
  try {
    return { res, json: JSON.parse(raw) as Envelope<T> };
  } catch {
    // Un 502 del proxy o un 500 sin capturar no responden JSON: se conserva el estado,
    // que es la unica pista util, y nunca se ensena el cuerpo crudo.
    throw new ApiError(res.status, { code: ERROR_CODES.INVALID_RESPONSE, message: '' });
  }
}

function codeOf(json: Envelope<unknown> | null): string {
  return json?.error?.code ?? '';
}

function toError(res: Response, json: Envelope<unknown> | null): ApiError {
  return new ApiError(res.status, json?.error ?? null, json);
}

async function request<T>(
  method: Method,
  path: string,
  opts?: RequestOptions,
): Promise<ApiResponse<T>> {
  const url = buildUrl(path, opts?.params);
  const headers: Record<string, string> = { Accept: 'application/json' };
  const body = opts?.body === undefined ? undefined : JSON.stringify(opts.body);
  if (body !== undefined) headers['Content-Type'] = 'application/json';

  const token = await getValidToken();
  if (token) headers.Authorization = `Bearer ${token}`;

  let { res, json } = await exchange<T>(method, url, headers, body, opts?.signal);

  if (res.status === 401) {
    const fresh = await refreshSingleFlight();
    if (fresh) {
      headers.Authorization = `Bearer ${fresh}`;
      ({ res, json } = await exchange<T>(method, url, headers, body, opts?.signal));
    }
    if (res.status === 401) {
      // Solo cae la sesion si la cookie tampoco sirve. Si la renovacion dio un token
      // valido, el 401 es de ESTA peticion y tumbar toda la aplicacion seria excesivo.
      if (!fresh) dropSession();
      throw toError(res, json);
    }
  }

  if (
    res.status === 403 &&
    codeOf(json) === ERROR_CODES.STEP_UP_REQUIRED &&
    !headers['X-Step-Up']
  ) {
    const stepUp = await ensureStepUpToken();
    if (stepUp) {
      headers['X-Step-Up'] = stepUp;
      ({ res, json } = await exchange<T>(method, url, headers, body, opts?.signal));
      if (res.status === 403 && codeOf(json) === ERROR_CODES.STEP_UP_REQUIRED) {
        clearStepUpToken();
      }
    }
  }

  if (!res.ok) throw toError(res, json);

  return { data: (json?.data ?? null) as T, meta: json?.meta };
}

export const api = {
  get: <T>(path: string, opts?: RequestOptions) => request<T>('GET', path, opts),
  post: <T>(path: string, opts?: RequestOptions) => request<T>('POST', path, opts),
  put: <T>(path: string, opts?: RequestOptions) => request<T>('PUT', path, opts),
  patch: <T>(path: string, opts?: RequestOptions) => request<T>('PATCH', path, opts),
  delete: <T>(path: string, opts?: RequestOptions) => request<T>('DELETE', path, opts),
};
