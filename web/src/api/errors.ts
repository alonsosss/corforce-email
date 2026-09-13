import type { ApiErrorBody } from './types';

// Codigos que el backend emite (pkg/response y handlers) mas los que el cliente HTTP
// fabrica cuando ni siquiera hay respuesta JSON. Las pantallas deciden por codigo, nunca
// por el texto del mensaje.
export const ERROR_CODES = {
  BAD_REQUEST: 'BAD_REQUEST',
  UNAUTHORIZED: 'UNAUTHORIZED',
  FORBIDDEN: 'FORBIDDEN',
  NOT_FOUND: 'NOT_FOUND',
  CONFLICT: 'CONFLICT',
  VALIDATION_ERROR: 'VALIDATION_ERROR',
  INTERNAL_ERROR: 'INTERNAL_ERROR',
  SERVICE_UNAVAILABLE: 'SERVICE_UNAVAILABLE',
  STEP_UP_REQUIRED: 'STEP_UP_REQUIRED',
  ACCOUNT_LOCKED: 'ACCOUNT_LOCKED',
  ACCOUNT_INACTIVE: 'ACCOUNT_INACTIVE',
  PASSWORD_POLICY: 'PASSWORD_POLICY',
  PASSWORD_REUSED: 'PASSWORD_REUSED',
  PASSWORD_BREACHED: 'PASSWORD_BREACHED',
  RESET_TOKEN_INVALID: 'RESET_TOKEN_INVALID',
  // Fabricados por el cliente: sin respuesta o respuesta que no es JSON.
  NETWORK_ERROR: 'NETWORK_ERROR',
  INVALID_RESPONSE: 'INVALID_RESPONSE',
} as const;

export type ErrorCode = (typeof ERROR_CODES)[keyof typeof ERROR_CODES];

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly body: unknown;

  constructor(status: number, error: ApiErrorBody | null, body?: unknown) {
    super(error?.message ?? `HTTP ${status}`);
    this.name = 'ApiError';
    this.status = status;
    this.code = error?.code ?? '';
    this.body = body;
  }

  is(code: ErrorCode): boolean {
    return this.code === code;
  }
}

export function isApiError(err: unknown): err is ApiError {
  return err instanceof ApiError;
}

export function errorCode(err: unknown): string {
  return isApiError(err) ? err.code : '';
}
