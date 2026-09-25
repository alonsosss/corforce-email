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
  // domain-service, publicacion automatica del DNS. El resto de sus codigos solo se muestran.
  DNS_PROVIDER_NOT_CONNECTED: 'DNS_PROVIDER_NOT_CONNECTED',
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
  // scheduler: zona del trabajo que no es un nombre IANA cargable, un one_time ya
  // despachado que se intenta reactivar (409), y una edicion con una version que ya no es la
  // guardada (409) o sin ella (428).
  INVALID_TIMEZONE: 'INVALID_TIMEZONE',
  JOB_ALREADY_RUN: 'JOB_ALREADY_RUN',
  VERSION_CONFLICT: 'VERSION_CONFLICT',
  VERSION_REQUIRED: 'VERSION_REQUIRED',
  // webmail (services/webmail/internal/adapters/http/handler.go, writeError). El resto de
  // sus codigos solo se muestran y se traducen con error.code.<CODIGO>.
  SESSION_EXPIRED: 'SESSION_EXPIRED',
  INVALID_CREDENTIALS: 'INVALID_CREDENTIALS',
  RECIPIENT_REJECTED: 'RECIPIENT_REJECTED',
  DELIVERY_UNCERTAIN: 'DELIVERY_UNCERTAIN',
  SEND_IN_PROGRESS: 'SEND_IN_PROGRESS',
  // Verificacion en dos pasos del buzon y reautenticacion (docs/Plan_Webmail_Seguridad.md).
  MFA_REQUIRED: 'MFA_REQUIRED',
  INVALID_MFA_CODE: 'INVALID_MFA_CODE',
  MFA_CHALLENGE_EXPIRED: 'MFA_CHALLENGE_EXPIRED',
  MFA_ALREADY_ENABLED: 'MFA_ALREADY_ENABLED',
  MFA_NOT_ENABLED: 'MFA_NOT_ENABLED',
  MFA_SETUP_EXPIRED: 'MFA_SETUP_EXPIRED',
  APP_PASSWORD_LIMIT: 'APP_PASSWORD_LIMIT',
  APP_PASSWORD_NOT_FOUND: 'APP_PASSWORD_NOT_FOUND',
  REAUTH_REQUIRED: 'REAUTH_REQUIRED',
  EXTERNAL_FORWARDING_DISABLED: 'EXTERNAL_FORWARDING_DISABLED',
  // Contactos y eventos: el If-Match ya no coincide (412), el recurso cambio en otro dispositivo.
  PRECONDITION_FAILED: 'PRECONDITION_FAILED',
  // Envio programado que ya esta saliendo o salio (409) o que ya no existe (404): la lista
  // estaba desfasada.
  SCHEDULED_SEND_NOT_PENDING: 'SCHEDULED_SEND_NOT_PENDING',
  SCHEDULED_SEND_NOT_FOUND: 'SCHEDULED_SEND_NOT_FOUND',
  // Pospuesto o seguimiento que ya volvio, se aviso o se cancelo en otro dispositivo.
  REMINDER_NOT_FOUND: 'REMINDER_NOT_FOUND',
  REMINDER_NOT_PENDING: 'REMINDER_NOT_PENDING',
  // templates: imagenes sin antivirus configurado (503) o rechazadas por el (422), y una
  // version de marketing que no pasa la verificacion de entregabilidad al publicar (409).
  SCANNER_UNAVAILABLE: 'SCANNER_UNAVAILABLE',
  ASSET_REJECTED: 'ASSET_REJECTED',
  DELIVERABILITY_FAILED: 'DELIVERABILITY_FAILED',
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

/** Un dato de error.details de la respuesta, si el servidor lo trajo. */
export function errorDetail(err: unknown, key: string): string | null {
  if (!isApiError(err) || typeof err.body !== 'object' || err.body === null) return null;
  const details = (err.body as { error?: { details?: unknown } }).error?.details;
  if (typeof details !== 'object' || details === null) return null;
  const value = (details as Record<string, unknown>)[key];
  return typeof value === 'string' && value ? value : null;
}

/** Una lista de textos de error.details (por ejemplo, las direcciones de un reenvio). */
export function errorDetailList(err: unknown, key: string): string[] {
  if (!isApiError(err) || typeof err.body !== 'object' || err.body === null) return [];
  const details = (err.body as { error?: { details?: unknown } }).error?.details;
  if (typeof details !== 'object' || details === null) return [];
  const value = (details as Record<string, unknown>)[key];
  if (!Array.isArray(value)) return [];
  return value.filter((item): item is string => typeof item === 'string' && item !== '');
}
