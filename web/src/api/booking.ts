import { apiBase, endpoints } from './endpoints';
import { ApiError, ERROR_CODES } from './errors';
import type { Envelope } from './types';

/*
 * Cliente de la pagina publica de citas (services/webmail, ruta publica del gateway). No hay
 * sesion de ningun tipo: ni la cookie del webmail ni el token de la plataforma viajan
 * (credentials: 'omit'). El visitante solo ve la pagina, sus huecos libres y su reserva.
 */

export interface BookingSlot {
  start: string;
  end: string;
}

export interface PublicBookingPage {
  title: string;
  description: string;
  duration_minutes: number;
  /** Zona en la que el dueno define sus franjas; los huecos llegan como instantes RFC 3339. */
  timezone: string;
  owner_name: string;
  min_notice_minutes: number;
  max_advance_days: number;
  slots: BookingSlot[];
}

export interface BookingRequest {
  start: string;
  name: string;
  email: string;
  note: string;
  /** Trampa para robots: la pagina lo oculta y una persona lo deja vacio. */
  website: string;
}

export interface BookingConfirmation {
  title: string;
  start: string;
  end: string;
  timezone: string;
  owner_name: string;
  confirmation_sent: boolean;
}

export interface BookingTarget {
  cell: string;
  tenant: string;
  page: string;
}

async function call<T>(
  method: 'GET' | 'POST',
  url: string,
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  let res: Response;
  try {
    res = await fetch(url, {
      method,
      headers:
        body === undefined
          ? { Accept: 'application/json' }
          : { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: 'omit',
      cache: 'no-store',
      signal,
    });
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') throw err;
    throw new ApiError(0, { code: ERROR_CODES.NETWORK_ERROR, message: '' });
  }
  let parsed: Envelope<T> | null = null;
  try {
    parsed = (await res.json()) as Envelope<T>;
  } catch {
    parsed = null;
  }
  if (!res.ok) {
    let code = parsed?.error?.code ?? '';
    if (!code && res.status === 429) code = ERROR_CODES.RATE_LIMITED;
    if (!code && parsed === null) code = ERROR_CODES.INVALID_RESPONSE;
    throw new ApiError(
      res.status,
      { code, message: parsed?.error?.message ?? '', details: parsed?.error?.details },
      parsed,
    );
  }
  if (!parsed || parsed.data === undefined) {
    throw new ApiError(res.status, { code: ERROR_CODES.INVALID_RESPONSE, message: '' });
  }
  return parsed.data;
}

export const bookingApi = {
  page: (target: BookingTarget, start: string, end: string, signal?: AbortSignal) => {
    const query = new URLSearchParams({ start, end }).toString();
    const url = `${apiBase()}${endpoints.publicBooking.page(target.cell, target.tenant, target.page)}?${query}`;
    return call<PublicBookingPage>('GET', url, undefined, signal);
  },
  book: (target: BookingTarget, request: BookingRequest) =>
    call<BookingConfirmation>(
      'POST',
      `${apiBase()}${endpoints.publicBooking.page(target.cell, target.tenant, target.page)}`,
      request,
    ),
};
