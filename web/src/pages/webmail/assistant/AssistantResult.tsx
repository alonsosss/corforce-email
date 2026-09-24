import type { ReactNode } from 'react';
import { Alert, Button, CopyButton } from '@/design/components';
import { IconX } from '@/design/icons';
import { t } from '@/i18n';

export interface AssistantResultProps {
  title: string;
  text: string;
  inputTruncated?: boolean;
  outputTruncated?: boolean;
  /** Acciones propias del resultado (insertar, reemplazar, responder). */
  actions?: ReactNode;
  /** Contenido estructurado bajo el texto (tareas y citas propuestas). */
  children?: ReactNode;
  onDiscard: () => void;
}

/**
 * Texto propuesto por el asistente. Se muestra como texto plano (nunca como HTML) con el aviso de
 * tratamiento: el usuario decide que hacer con el.
 */
export function AssistantResult({
  title,
  text,
  inputTruncated = false,
  outputTruncated = false,
  actions,
  children,
  onDiscard,
}: AssistantResultProps) {
  return (
    <section className="cf-wm-assistant__result" aria-label={title}>
      <header className="cf-wm-assistant__result-head">
        <strong>{title}</strong>
        <Button
          size="sm"
          variant="ghost"
          iconOnly
          title={t('webmail.assistant.discard')}
          icon={<IconX size={14} />}
          onClick={onDiscard}
        >
          {t('webmail.assistant.discard')}
        </Button>
      </header>
      {text ? <div className="cf-wm-assistant__text">{text}</div> : null}
      {children}
      {inputTruncated ? <Alert tone="info">{t('webmail.assistant.inputTruncated')}</Alert> : null}
      {outputTruncated ? (
        <Alert tone="warning">{t('webmail.assistant.outputTruncated')}</Alert>
      ) : null}
      {text || actions ? (
        <div className="cf-wm-assistant__actions">
          {text ? <CopyButton value={text} withText /> : null}
          {actions}
        </div>
      ) : null}
      <p className="cf-wm-assistant__privacy cf-text-sm cf-text-muted">
        {t('webmail.assistant.privacy')}
      </p>
    </section>
  );
}
