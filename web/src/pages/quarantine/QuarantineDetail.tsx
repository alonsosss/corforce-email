import { useState } from 'react';
import { mailSecurityApi, type QuarantineItem } from '@/api/mailSecurity';
import { errorMessage } from '@/api/messages';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { Alert, Badge, Button, DescriptionList, Modal, useToast } from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { formatBytes } from '@/lib/quota';
import { t, type MessageKey } from '@/i18n';
import { QuarantineMessage } from './QuarantineMessageView';

type Operation = 'release' | 'learn' | 'delete';

const CONFIRM: Record<Operation, { message: MessageKey; done: MessageKey; label: MessageKey }> = {
  release: {
    message: 'quarantine.releaseConfirm',
    done: 'quarantine.released',
    label: 'quarantine.release',
  },
  learn: {
    message: 'quarantine.learnConfirm',
    done: 'quarantine.learned',
    label: 'quarantine.learnSpam',
  },
  delete: {
    message: 'quarantine.deleteConfirm',
    done: 'quarantine.deleted',
    label: 'common.delete',
  },
};

export interface QuarantineDetailProps {
  item: QuarantineItem;
  onClose: () => void;
  /** Tras liberar o borrar: el elemento ya no esta en la cuarentena. */
  onRemoved: () => void;
}

/**
 * La confirmacion va dentro del mismo modal: dos dialogos apilados se cerrarian juntos con
 * Escape.
 */
export function QuarantineDetail({ item, onClose, onRemoved }: QuarantineDetailProps) {
  const toast = useToast();
  const { can } = useAccess();
  const [pending, setPending] = useState<Operation | null>(null);
  const [showMessage, setShowMessage] = useState(false);

  const run = useAction(async (operation: Operation) => {
    if (operation === 'release') await mailSecurityApi.releaseQuarantine(item.id);
    else if (operation === 'learn') await mailSecurityApi.learnSpam(item.id);
    else await mailSecurityApi.deleteQuarantine(item.id);
  });

  const confirm = async () => {
    if (!pending) return;
    const operation = pending;
    if (!(await run.run(operation))) return;
    toast.success(t(CONFIRM[operation].done));
    setPending(null);
    if (operation !== 'learn') onRemoved();
  };

  const footer = pending ? (
    <>
      <span className="cf-text-sm cf-text-secondary" style={{ marginRight: 'auto' }}>
        {t(CONFIRM[pending].message)}
      </span>
      <Button onClick={() => setPending(null)} disabled={run.busy}>
        {t('common.cancel')}
      </Button>
      <Button
        variant={pending === 'delete' ? 'danger' : 'primary'}
        loading={run.busy}
        onClick={() => void confirm()}
      >
        {t(CONFIRM[pending].label)}
      </Button>
    </>
  ) : (
    <>
      <Button onClick={() => setShowMessage((v) => !v)} style={{ marginRight: 'auto' }}>
        {showMessage ? t('quarantine.hideMessage') : t('quarantine.showMessage')}
      </Button>
      {can(...PERMISSIONS.quarantine.learn) ? (
        <Button onClick={() => setPending('learn')}>{t('quarantine.learnSpam')}</Button>
      ) : null}
      {can(...PERMISSIONS.quarantine.delete) ? (
        <Button variant="danger" onClick={() => setPending('delete')}>
          {t('common.delete')}
        </Button>
      ) : null}
      {can(...PERMISSIONS.quarantine.release) ? (
        <Button variant="primary" onClick={() => setPending('release')}>
          {t('quarantine.release')}
        </Button>
      ) : null}
    </>
  );

  return (
    <Modal open size="lg" title={t('quarantine.detailTitle')} onClose={onClose} footer={footer}>
      <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
        {pending === 'release' ? (
          <Alert tone="warning">{t('quarantine.releaseWarning')}</Alert>
        ) : null}
        {run.error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(run.error)}
          </div>
        ) : null}
        <DescriptionList
          items={[
            { label: t('quarantine.column.subject'), value: item.subject || t('common.dash') },
            {
              label: t('quarantine.column.sender'),
              value: <span className="cf-mono">{item.sender}</span>,
            },
            {
              label: t('quarantine.column.rcpt'),
              value: <span className="cf-mono">{item.rcpt}</span>,
            },
            {
              label: t('quarantine.column.score'),
              value: <Badge tone="warning">{item.score}</Badge>,
            },
            { label: t('quarantine.column.action'), value: item.action },
            {
              label: t('quarantine.ip'),
              value: <span className="cf-mono">{item.ip || t('common.dash')}</span>,
            },
            { label: t('quarantine.column.size'), value: formatBytes(item.size) },
            { label: t('quarantine.column.when'), value: formatDateTime(item.created_at) },
            {
              label: t('quarantine.notified'),
              value: item.notified ? t('common.yes') : t('common.no'),
            },
            { label: t('quarantine.qid'), value: <span className="cf-mono">{item.qid}</span> },
          ]}
        />
        <div className="cf-stack" style={{ gap: 'var(--cf-space-2)' }}>
          <div className="cf-field__label">{t('quarantine.symbols')}</div>
          {item.symbols?.length ? (
            <div className="cf-inline-list">
              {item.symbols.map((symbol) => (
                <Badge key={symbol}>
                  <span className="cf-mono">{symbol}</span>
                </Badge>
              ))}
            </div>
          ) : (
            <span className="cf-text-muted cf-text-sm">{t('common.none')}</span>
          )}
        </div>
        {showMessage ? <QuarantineMessage id={item.id} /> : null}
      </div>
    </Modal>
  );
}
