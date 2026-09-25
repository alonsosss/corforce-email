import { useState } from 'react';
import { mailPolicyApi } from '@/api/mailDirectory';
import { errorMessage } from '@/api/messages';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import {
  Alert,
  Badge,
  Button,
  Card,
  ConfirmDialog,
  DescriptionList,
  ErrorState,
  Skeleton,
  useToast,
} from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { t } from '@/i18n';

/**
 * Reenvio a direcciones de fuera de la empresa (mail-directory, politica por empresa). Permitido
 * por defecto. Apagarlo retira en el acto los destinos externos de los reenvios y reglas ya
 * guardados de todos los buzones, por eso pide confirmacion; volver a permitirlo no restaura
 * nada.
 */
export function ExternalForwardingCard() {
  const { can } = useAccess();
  const toast = useToast();
  const [confirming, setConfirming] = useState(false);
  const policy = useQuery(async () => (await mailPolicyApi.get()).data, []);
  const canUpdate = can(...PERMISSIONS.mailPolicy.update);

  const allow = useAction(async () => {
    const { data } = await mailPolicyApi.set(true);
    policy.setData(data);
    toast.success(t('mailboxes.forwardingPolicy.allowed'));
  });

  if (policy.error) {
    return (
      <Card title={t('mailboxes.forwardingPolicy.title')}>
        <ErrorState error={policy.error} onRetry={policy.reload} />
      </Card>
    );
  }
  if (!policy.data) {
    return (
      <Card title={t('mailboxes.forwardingPolicy.title')}>
        <Skeleton lines={3} />
      </Card>
    );
  }

  const current = policy.data;
  return (
    <Card
      title={t('mailboxes.forwardingPolicy.title')}
      description={t('mailboxes.forwardingPolicy.description')}
    >
      <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
        <DescriptionList
          items={[
            {
              label: t('mailboxes.forwardingPolicy.status'),
              value: (
                <Badge tone={current.external_forwarding_allowed ? 'success' : 'neutral'}>
                  {t(
                    current.external_forwarding_allowed
                      ? 'mailboxes.forwardingPolicy.on'
                      : 'mailboxes.forwardingPolicy.off',
                  )}
                </Badge>
              ),
            },
            {
              label: t('mailboxes.forwardingPolicy.updatedAt'),
              value: current.updated_at ? formatDateTime(current.updated_at) : t('common.dash'),
            },
          ]}
        />
        {current.external_forwarding_allowed ? (
          <Alert>{t('mailboxes.forwardingPolicy.removalNotice')}</Alert>
        ) : null}
        {allow.error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(allow.error)}
          </div>
        ) : null}
        {canUpdate ? (
          <div>
            {current.external_forwarding_allowed ? (
              <Button variant="danger" onClick={() => setConfirming(true)}>
                {t('mailboxes.forwardingPolicy.disable')}
              </Button>
            ) : (
              <Button loading={allow.busy} onClick={() => void allow.run()}>
                {t('mailboxes.forwardingPolicy.enable')}
              </Button>
            )}
          </div>
        ) : (
          <p className="cf-text-muted cf-text-sm">{t('mailboxes.forwardingPolicy.readOnly')}</p>
        )}
      </div>
      <ConfirmDialog
        open={confirming}
        title={t('mailboxes.forwardingPolicy.disableTitle')}
        message={t('mailboxes.forwardingPolicy.disableConfirm')}
        confirmLabel={t('mailboxes.forwardingPolicy.disable')}
        danger
        onCancel={() => setConfirming(false)}
        onConfirm={async () => {
          const { data } = await mailPolicyApi.set(false);
          policy.setData(data);
          toast.success(t('mailboxes.forwardingPolicy.disabled'));
          setConfirming(false);
        }}
      />
    </Card>
  );
}
