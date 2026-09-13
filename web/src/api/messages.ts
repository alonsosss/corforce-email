import { ERROR_CODES, isApiError } from './errors';
import { t, type MessageKey } from '@/i18n';

// Codigo del backend -> texto de la aplicacion. Los codigos sin entrada muestran el
// mensaje del servidor (validaciones, conflictos), que explica el caso concreto.
const BY_CODE: Partial<Record<string, MessageKey>> = {
  [ERROR_CODES.NETWORK_ERROR]: 'error.network',
  [ERROR_CODES.INVALID_RESPONSE]: 'error.invalidResponse',
  [ERROR_CODES.UNAUTHORIZED]: 'error.unauthorized',
  [ERROR_CODES.FORBIDDEN]: 'error.forbidden',
  [ERROR_CODES.NOT_FOUND]: 'error.notFound',
  [ERROR_CODES.INTERNAL_ERROR]: 'error.internal',
  [ERROR_CODES.SERVICE_UNAVAILABLE]: 'error.serviceUnavailable',
  [ERROR_CODES.STEP_UP_REQUIRED]: 'error.stepUpCancelled',
  [ERROR_CODES.ACCOUNT_LOCKED]: 'error.accountLocked',
  [ERROR_CODES.ACCOUNT_INACTIVE]: 'error.accountInactive',
  [ERROR_CODES.PASSWORD_POLICY]: 'error.passwordPolicy',
  [ERROR_CODES.PASSWORD_REUSED]: 'error.passwordReused',
  [ERROR_CODES.PASSWORD_BREACHED]: 'error.passwordBreached',
  [ERROR_CODES.RESET_TOKEN_INVALID]: 'error.resetTokenInvalid',
  [ERROR_CODES.INTEGRATION_UNAVAILABLE]: 'error.integrationUnavailable',
  [ERROR_CODES.NOT_CONFIGURED]: 'error.notConfigured',
  [ERROR_CODES.UNSUBSCRIBE_PROTECTED]: 'error.unsubscribeProtected',
};

export function errorMessage(
  err: unknown,
  overrides?: Partial<Record<string, MessageKey>>,
): string {
  if (isApiError(err)) {
    const override = overrides?.[err.code];
    if (override) return t(override);
    const key = BY_CODE[err.code];
    if (key) return t(key);
    if (err.message) return err.message;
    if (err.status >= 502 && err.status <= 504) return t('error.serviceUnavailable');
  }
  return t('error.generic');
}
