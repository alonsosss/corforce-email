import { useState } from 'react';
import type { WebmailFolder } from '@/api/webmail';
import { errorMessage } from '@/api/messages';
import { Button, FormField, Modal, Select } from '@/design/components';
import { t } from '@/i18n';
import { orderFolders } from './folders';

/** Elegir la carpeta de destino de uno o varios mensajes. */
export function MoveDialog({
  title,
  folders,
  current,
  onClose,
  onMove,
}: {
  title: string;
  folders: readonly WebmailFolder[];
  current: string;
  onClose: () => void;
  onMove: (to: string, label: string) => Promise<void>;
}) {
  const targets = orderFolders(folders).filter(
    (item) => item.folder.selectable && item.folder.name !== current,
  );
  const [to, setTo] = useState(targets[0]?.folder.name ?? '');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const target = targets.find((item) => item.folder.name === to);

  const submit = async () => {
    if (!target) return;
    setBusy(true);
    setError(null);
    try {
      await onMove(target.folder.name, target.label);
    } catch (err) {
      setError(err);
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      title={title}
      onClose={() => (busy ? undefined : onClose())}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button variant="primary" loading={busy} disabled={!target} onClick={() => void submit()}>
            {t('webmail.move.submit')}
          </Button>
        </>
      }
    >
      <div className="cf-stack" style={{ gap: 'var(--cf-space-3)' }}>
        {targets.length ? (
          <FormField label={t('webmail.move.to')} htmlFor="wm-move-to">
            <Select
              id="wm-move-to"
              options={targets.map((item) => ({
                value: item.folder.name,
                label: `${'  '.repeat(item.depth)}${item.label}`,
              }))}
              value={to}
              onChange={(e) => setTo(e.target.value)}
            />
          </FormField>
        ) : (
          <p className="cf-modal__message">{t('webmail.move.none')}</p>
        )}
        {error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(error)}
          </div>
        ) : null}
      </div>
    </Modal>
  );
}
