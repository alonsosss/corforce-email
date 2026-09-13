import { useState, type ReactNode } from 'react';
import { Modal } from './Modal';
import { Button } from './Button';
import { errorMessage } from '@/api/messages';
import { t, type MessageKey } from '@/i18n';

export interface ConfirmDialogProps {
  open: boolean;
  title: string;
  message: ReactNode;
  confirmLabel?: string;
  danger?: boolean;
  onConfirm: () => Promise<void>;
  onCancel: () => void;
  /** Texto propio para un codigo de error concreto del backend (por ejemplo, un 409). */
  errorOverrides?: Partial<Record<string, MessageKey>>;
}

/**
 * Confirmacion con la accion dentro: ejecuta onConfirm, muestra su error si falla y solo
 * se cierra cuando termina bien. El padre no tiene que llevar estado de carga ni de error.
 */
export function ConfirmDialog({
  open,
  title,
  message,
  confirmLabel,
  danger = false,
  onConfirm,
  onCancel,
  errorOverrides,
}: ConfirmDialogProps) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);

  const cancel = () => {
    if (busy) return;
    setError(null);
    onCancel();
  };

  const confirm = async () => {
    setBusy(true);
    setError(null);
    try {
      await onConfirm();
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open={open}
      title={title}
      onClose={cancel}
      footer={
        <>
          <Button onClick={cancel} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button
            variant={danger ? 'danger' : 'primary'}
            loading={busy}
            onClick={() => void confirm()}
          >
            {confirmLabel ?? t('common.confirm')}
          </Button>
        </>
      }
    >
      <div className="cf-stack" style={{ gap: 'var(--cf-space-3)' }}>
        <p className="cf-modal__message">{message}</p>
        {error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(error, errorOverrides)}
          </div>
        ) : null}
      </div>
    </Modal>
  );
}
