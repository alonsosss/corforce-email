import { ERROR_CODES, errorCode, errorDetail } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import { hasMessage, t } from '@/i18n';

/**
 * Texto de un error de contactos o calendario. Los topes de mail-dav (507 y 413) traen en
 * details.limit cual se supero: cuota de contactos, eventos o espacio, o tamano de la
 * peticion o de la importacion.
 */
export function davErrorMessage(err: unknown): string {
  const limit = errorDetail(err, 'limit');
  const key = limit ? `webmail.dav.limit.${limit}` : '';
  if (key && hasMessage(key)) return t(key);
  return errorMessage(err);
}

/** Campo (details.field) de un 422 de validacion; null si el error es de otro tipo. */
export function davErrorField(err: unknown): string | null {
  return errorCode(err) === ERROR_CODES.VALIDATION_ERROR ? errorDetail(err, 'field') : null;
}
