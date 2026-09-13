import { codeMessage, errorMessage } from '@/api/messages';
import { isApiError } from '@/api/errors';
import type { MessageKey } from '@/i18n';

/** Codigos propios de services/automations (writeError del handler) y su texto. */
export const AUTOMATION_ERRORS: Partial<Record<string, MessageKey>> = {
  NAME_TAKEN: 'automations.error.NAME_TAKEN',
  NOT_EDITABLE: 'automations.error.NOT_EDITABLE',
  NOT_DELETABLE: 'automations.error.NOT_DELETABLE',
  INVALID_TRANSITION: 'automations.error.INVALID_TRANSITION',
  CONCURRENT_CHANGE: 'automations.error.CONCURRENT_CHANGE',
  TEMPLATE_NOT_PUBLISHED: 'automations.error.TEMPLATE_NOT_PUBLISHED',
  TEMPLATE_MISSING_CONFIRM_URL: 'automations.error.TEMPLATE_MISSING_CONFIRM_URL',
  TEMPLATE_VARIABLES: 'automations.error.TEMPLATE_VARIABLES',
  TEMPLATE_VERSION_REQUIRED: 'automations.error.TEMPLATE_VERSION_REQUIRED',
};

/**
 * Error de una accion de automations: el texto del codigo y, cuando el codigo tiene texto
 * propio, el mensaje del servicio como detalle (dice que paso falla: "steps[2]: ...").
 */
export function ActionError({ error }: { error: unknown }) {
  if (!error) return null;
  const text = errorMessage(error, AUTOMATION_ERRORS);
  const specific =
    isApiError(error) &&
    error.code !== '' &&
    (AUTOMATION_ERRORS[error.code] !== undefined || codeMessage(error.code) !== null);
  const detail = specific && error.message && error.message !== text ? error.message : null;
  return (
    <div className="cf-form__error" role="alert">
      <div>{text}</div>
      {detail ? <div className="cf-text-sm cf-break">{detail}</div> : null}
    </div>
  );
}
