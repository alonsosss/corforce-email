import { useState } from 'react';
import { assistantSettingsApi } from '@/api/webmailAssistant';
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
 * Interruptor del asistente del webmail para la empresa (mail-directory, docs/adr/0015). Apagado por
 * defecto; activarlo exige aceptar el aviso de tratamiento de datos, porque el texto que cada usuario
 * pida procesar sale a un proveedor externo. Apagarlo no pide confirmacion.
 *
 * Si la plataforma tiene o no la clave del proveedor solo lo sabe el webmail (GET /webmail/assistant,
 * reason not_configured), con la sesion del buzon; mail-directory no lo expone, asi que aqui solo se
 * avisa de la dependencia.
 */
export function AssistantSettingsCard() {
  const { can } = useAccess();
  const toast = useToast();
  const [notice, setNotice] = useState(false);
  const settings = useQuery(async () => (await assistantSettingsApi.get()).data, []);
  const canUpdate = can(...PERMISSIONS.assistantSettings.update);

  const disable = useAction(async () => {
    const { data } = await assistantSettingsApi.set(false);
    settings.setData(data);
    toast.success(t('mailboxes.assistant.disabled'));
  });

  if (settings.error) {
    return (
      <Card title={t('mailboxes.assistant.title')}>
        <ErrorState error={settings.error} onRetry={settings.reload} />
      </Card>
    );
  }
  if (!settings.data) {
    return (
      <Card title={t('mailboxes.assistant.title')}>
        <Skeleton lines={3} />
      </Card>
    );
  }

  const current = settings.data;
  return (
    <Card title={t('mailboxes.assistant.title')} description={t('mailboxes.assistant.description')}>
      <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
        <DescriptionList
          items={[
            {
              label: t('mailboxes.assistant.status'),
              value: (
                <Badge tone={current.enabled ? 'success' : 'neutral'}>
                  {t(current.enabled ? 'mailboxes.assistant.on' : 'mailboxes.assistant.off')}
                </Badge>
              ),
            },
            {
              label: t('mailboxes.assistant.enabledAt'),
              value: current.enabled ? formatDateTime(current.enabled_at) : t('common.dash'),
            },
          ]}
        />
        <Alert title={t('mailboxes.assistant.providerTitle')}>
          {t('mailboxes.assistant.providerNotice')}
        </Alert>
        {disable.error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(disable.error)}
          </div>
        ) : null}
        {canUpdate ? (
          <div>
            {current.enabled ? (
              <Button loading={disable.busy} onClick={() => void disable.run()}>
                {t('mailboxes.assistant.disable')}
              </Button>
            ) : (
              <Button variant="primary" onClick={() => setNotice(true)}>
                {t('mailboxes.assistant.enable')}
              </Button>
            )}
          </div>
        ) : (
          <p className="cf-text-muted cf-text-sm">{t('mailboxes.assistant.readOnly')}</p>
        )}
      </div>
      <ConfirmDialog
        open={notice}
        title={t('mailboxes.assistant.noticeTitle')}
        message={t('mailboxes.assistant.notice')}
        confirmLabel={t('mailboxes.assistant.accept')}
        onCancel={() => setNotice(false)}
        onConfirm={async () => {
          const { data } = await assistantSettingsApi.set(true);
          settings.setData(data);
          toast.success(t('mailboxes.assistant.enabled'));
          setNotice(false);
        }}
      />
    </Card>
  );
}
