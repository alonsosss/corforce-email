import type { CreatedApiKey, SmtpSettings } from '@/api/apiKeys';
import { apiBase, endpoints } from '@/api/endpoints';
import { Alert, Button, CopyButton, DescriptionList, Modal } from '@/design/components';
import { t } from '@/i18n';

/** URL absoluta del envio transaccional: el gateway sirve el API en el mismo origen. */
export function transactionalMessagesUrl(): string {
  return new URL(`${apiBase()}${endpoints.transactional.messages}`, window.location.origin).href;
}

function Secret({ value, label }: { value: string; label: string }) {
  return (
    <div className="cf-secret">
      <span className="cf-secret__value" aria-label={label}>
        {value}
      </span>
      <CopyButton value={value} withText />
    </div>
  );
}

/**
 * El secreto solo existe en la respuesta del alta: el servidor guarda su resumen. Se
 * muestra una vez, sin cierre accidental, y quien abre el dialogo lo descarta al cerrarlo.
 */
export function ApiKeySecretDialog({
  result,
  smtp,
  onClose,
}: {
  result: CreatedApiKey;
  smtp: SmtpSettings | null;
  onClose: () => void;
}) {
  const header = t('apiKeys.secret.apiHeader', { secret: result.secret });
  return (
    <Modal
      open
      size="lg"
      dismissible={false}
      title={t('apiKeys.secret.title', { name: result.key.name })}
      onClose={onClose}
      footer={
        <Button variant="primary" onClick={onClose}>
          {t('apiKeys.secret.done')}
        </Button>
      }
    >
      <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
        <Alert tone="warning" title={t('apiKeys.secret.warningTitle')}>
          {t('apiKeys.secret.warning')}
        </Alert>
        <Secret value={result.secret} label={t('apiKeys.secret.valueLabel')} />

        <div className="cf-form__section">{t('apiKeys.secret.apiTitle')}</div>
        <p className="cf-text-muted cf-text-sm">{t('apiKeys.secret.apiDescription')}</p>
        <DescriptionList
          items={[
            {
              label: t('apiKeys.secret.apiEndpoint'),
              value: (
                <code className="cf-mono">
                  {t('apiKeys.secret.apiMethod')} {transactionalMessagesUrl()}
                </code>
              ),
            },
          ]}
        />
        <Secret value={header} label={t('apiKeys.secret.apiHeaderLabel')} />

        {smtp ? (
          <>
            <div className="cf-form__section">{t('apiKeys.smtp.title')}</div>
            <DescriptionList
              items={[
                {
                  label: t('apiKeys.smtp.host'),
                  value: <code className="cf-mono">{smtp.host}</code>,
                },
                {
                  label: t('apiKeys.smtp.starttlsPort'),
                  value: <code className="cf-mono">{smtp.starttls_port}</code>,
                },
                {
                  label: t('apiKeys.smtp.tlsPort'),
                  value: <code className="cf-mono">{smtp.tls_port}</code>,
                },
                {
                  label: t('apiKeys.smtp.username'),
                  value: (
                    <span className="cf-copy-cell">
                      <code className="cf-mono">{result.key.prefix}</code>
                      <CopyButton value={result.key.prefix} />
                    </span>
                  ),
                },
                { label: t('apiKeys.smtp.password'), value: t('apiKeys.smtp.passwordIsKey') },
              ]}
            />
          </>
        ) : null}
      </div>
    </Modal>
  );
}
