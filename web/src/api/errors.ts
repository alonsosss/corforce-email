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
  // Propios de un servicio: domain-service, mail-security y suppression.
  INTEGRATION_UNAVAILABLE: 'INTEGRATION_UNAVAILABLE',
  NOT_CONFIGURED: 'NOT_CONFIGURED',
  UNSUBSCRIBE_PROTECTED: 'UNSUBSCRIBE_PROTECTED',
  // contacts.
  CONTACT_EXISTS: 'CONTACT_EXISTS',
  LIST_EXISTS: 'LIST_EXISTS',
  SEGMENT_EXISTS: 'SEGMENT_EXISTS',
  ATTRIBUTE_EXISTS: 'ATTRIBUTE_EXISTS',
  LIST_IN_USE: 'LIST_IN_USE',
  ATTRIBUTE_IN_USE: 'ATTRIBUTE_IN_USE',
  RESUBSCRIBE_REQUIRES_OPT_IN: 'RESUBSCRIBE_REQUIRES_OPT_IN',
  CONSENT_ALREADY_GRANTED: 'CONSENT_ALREADY_GRANTED',
  CONTACT_NOT_REACHABLE: 'CONTACT_NOT_REACHABLE',
  QUERY_TIMEOUT: 'QUERY_TIMEOUT',
  // campaigns y los vecinos cuyo codigo reenvia (transactional, reputation, billing).
  RATE_LIMITED: 'RATE_LIMITED',
  DEPENDENCY_UNAVAILABLE: 'DEPENDENCY_UNAVAILABLE',
  SEND_REJECTED: 'SEND_REJECTED',
  SENDING_RESTRICTED: 'SENDING_RESTRICTED',
  PLAN_LIMIT_REACHED: 'PLAN_LIMIT_REACHED',
  // billing.
  NO_SUBSCRIPTION: 'NO_SUBSCRIPTION',
  PLAN_CODE_TAKEN: 'PLAN_CODE_TAKEN',
  PLAN_IN_USE: 'PLAN_IN_USE',
  PLAN_RETIRED: 'PLAN_RETIRED',
  // webmail (services/webmail/internal/adapters/http/handler.go, writeError). El resto de
  // sus codigos solo se muestran y se traducen con error.code.<CODIGO>.
  SESSION_EXPIRED: 'SESSION_EXPIRED',
  INVALID_CREDENTIALS: 'INVALID_CREDENTIALS',
  RECIPIENT_REJECTED: 'RECIPIENT_REJECTED',
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
