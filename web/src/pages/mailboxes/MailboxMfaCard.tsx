import { useState } from 'react';
import { mailDirectoryApi, type Mailbox } from '@/api/mailDirectory';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { Badge, Button, Card, ConfirmDialog, DescriptionList, useToast } from '@/design/components';
import { t } from '@/i18n';

/**
 * Verificacion en dos pasos del webmail del buzon. El usuario la activa desde su webmail; aqui
 * solo se restablece (quien perdio el movil y los codigos de recuperacion). Restablecerla cierra
 * sus sesiones del webmail; si la plataforma exige confirmar la identidad, el cliente HTTP la
 * pide como en cualquier accion critica.
 */
export function MailboxMfaCard({
  mailbox,
  onChange,
}: {
  mailbox: Mailbox;
  onChange: (next: Mailbox) => void;
}) {
  const { can } = useAccess();
  const toast = useToast();
  const [resetting, setResetting] = useState(false);
  const canReset = can(...PERMISSIONS.mailboxMfa.delete);

  return (
    <Card title={t('mailboxes.mfa.title')} description={t('mailboxes.mfa.description')}>
      <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
        <DescriptionList
          items={[
            {
              label: t('mailboxes.mfa.status'),
              value: (
                <Badge tone={mailbox.mfa_enabled ? 'success' : 'neutral'}>
                  {t(mailbox.mfa_enabled ? 'mailboxes.mfa.on' : 'mailboxes.mfa.off')}
                </Badge>
              ),
            },
          ]}
        />
        {mailbox.mfa_enabled && canReset ? (
          <div>
            <Button variant="danger" onClick={() => setResetting(true)}>
              {t('mailboxes.mfa.reset')}
            </Button>
          </div>
        ) : null}
      </div>
      <ConfirmDialog
        open={resetting}
        title={t('mailboxes.mfa.resetTitle')}
        message={t('mailboxes.mfa.resetConfirm', { address: mailbox.username })}
        confirmLabel={t('mailboxes.mfa.reset')}
        danger
        onCancel={() => setResetting(false)}
        onConfirm={async () => {
          await mailDirectoryApi.resetMailboxMfa(mailbox.id);
          onChange({ ...mailbox, mfa_enabled: false });
          toast.success(t('mailboxes.mfa.resetDone'));
          setResetting(false);
        }}
      />
    </Card>
  );
}
