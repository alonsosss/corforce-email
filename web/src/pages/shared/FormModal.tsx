import type { ReactNode } from 'react';
import { errorMessage } from '@/api/messages';
import { Button, Modal } from '@/design/components';
import { t, type MessageKey } from '@/i18n';

export interface FormModalProps {
  id: string;
  title: string;
  submitLabel: string;
  busy: boolean;
  error: unknown;
  onClose: () => void;
  onSubmit: () => void | Promise<void>;
  size?: 'md' | 'lg';
  errorOverrides?: Partial<Record<string, MessageKey>>;
  /** Envio imposible por un motivo que el formulario ya explica (por ejemplo, sin catalogo). */
  submitDisabled?: boolean;
  children: ReactNode;
}

/** Formulario en un modal con Cancelar y enviar, y el error del backend al pie. */
export function FormModal({
  id,
  title,
  submitLabel,
  busy,
  error,
  onClose,
  onSubmit,
  size,
  errorOverrides,
  submitDisabled = false,
  children,
}: FormModalProps) {
  return (
    <Modal
      open
      title={title}
      size={size}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button
            type="submit"
            form={id}
            variant="primary"
            loading={busy}
            disabled={submitDisabled}
          >
            {submitLabel}
          </Button>
        </>
      }
    >
      <form
        id={id}
        className="cf-form"
        noValidate
        onSubmit={(e) => {
          e.preventDefault();
          if (submitDisabled) return;
          void onSubmit();
        }}
      >
        {children}
        {error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(error, errorOverrides)}
          </div>
        ) : null}
      </form>
    </Modal>
  );
}
