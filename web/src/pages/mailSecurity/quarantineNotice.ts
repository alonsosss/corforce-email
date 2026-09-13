import { ERROR_CODES, isApiError } from '@/api/errors';

/** Campos del aviso que mail-security valida al guardar con el aviso activo. */
export type NoticeField = 'sender' | 'subject' | 'html_template';

export interface NoticeFieldError {
  field: NoticeField;
  message: string;
}

// domain.ValidateQuarantineNotify responde 422 VALIDATION_ERROR con el campo al principio
// del mensaje ("notify.sender ...", "notify.subject ...", "notify.html_template ..."). No
// hay un codigo por campo: el prefijo es el contrato que se puede leer.
const NOTICE_FIELD = /^notify\.(sender|subject|html_template)\b/;

/** El error del aviso asociado a su campo, o null si el error no es de un campo del aviso. */
export function noticeFieldError(err: unknown): NoticeFieldError | null {
  if (!isApiError(err) || err.code !== ERROR_CODES.VALIDATION_ERROR) return null;
  const match = NOTICE_FIELD.exec(err.message);
  const field = match?.[1] as NoticeField | undefined;
  return field ? { field, message: err.message } : null;
}
