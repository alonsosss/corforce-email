import { useState, type FormEvent } from 'react';
import { DNS_PROVIDER_CLOUDFLARE, dnsProvidersApi, type DnsProviderStatus } from '@/api/domains';
import { errorMessage } from '@/api/messages';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import {
  Badge,
  Button,
  Card,
  ConfirmDialog,
  DescriptionList,
  ErrorState,
  FormField,
  Input,
  Modal,
  Skeleton,
  useToast,
} from '@/design/components';
import { IconLink, IconTrash } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { t } from '@/i18n';

/** Zonas que se nombran en la ficha; el resto se cuenta. */
const ZONES_SHOWN = 5;

/**
 * Conexion de la empresa con Cloudflare. El token solo existe en el formulario mientras se escribe:
 * se envia una vez, se descarta al terminar y la plataforma nunca lo devuelve.
 */
export function DnsProviderCard() {
  const { can } = useAccess();
  const toast = useToast();
  const [connecting, setConnecting] = useState(false);
  const [disconnecting, setDisconnecting] = useState(false);
  const status = useQuery(
    async () => (await dnsProvidersApi.status(DNS_PROVIDER_CLOUDFLARE)).data,
    [],
  );

  const canConnect = can(...PERMISSIONS.dnsProviders.connect);
  const canDisconnect = can(...PERMISSIONS.dnsProviders.disconnect);
  const connected = status.data?.connected === true;

  return (
    <Card
      title={t('domains.dnsProvider.title')}
      description={t('domains.dnsProvider.description')}
      actions={
        status.data ? (
          <>
            {canConnect ? (
              <Button
                variant={connected ? 'secondary' : 'primary'}
                icon={<IconLink size={16} />}
                onClick={() => setConnecting(true)}
              >
                {connected ? t('domains.dnsProvider.replaceToken') : t('domains.dnsProvider.connect')}
              </Button>
            ) : null}
            {connected && canDisconnect ? (
              <Button
                variant="danger"
                icon={<IconTrash size={16} />}
                onClick={() => setDisconnecting(true)}
              >
                {t('domains.dnsProvider.disconnect')}
              </Button>
            ) : null}
          </>
        ) : null
      }
    >
      {status.error ? (
        <ErrorState error={status.error} onRetry={status.reload} />
      ) : !status.data ? (
        <Skeleton lines={3} />
      ) : connected ? (
        <ConnectionDetails status={status.data} />
      ) : (
        <p className="cf-text-muted">{t('domains.dnsProvider.notConnected')}</p>
      )}
      {connecting ? (
        <ConnectForm
          onClose={() => setConnecting(false)}
          onConnected={(next) => {
            status.setData(next);
            setConnecting(false);
            toast.success(t('domains.dnsProvider.connected'));
          }}
        />
      ) : null}
      <ConfirmDialog
        open={disconnecting}
        title={t('domains.dnsProvider.disconnect')}
        message={t('domains.dnsProvider.disconnectConfirm')}
        confirmLabel={t('domains.dnsProvider.disconnect')}
        danger
        onCancel={() => setDisconnecting(false)}
        onConfirm={async () => {
          const { data } = await dnsProvidersApi.disconnect(DNS_PROVIDER_CLOUDFLARE);
          status.setData({ provider: data.provider, connected: false });
          setDisconnecting(false);
          toast.success(t('domains.dnsProvider.disconnected'));
        }}
      />
    </Card>
  );
}

function ConnectionDetails({ status }: { status: DnsProviderStatus }) {
  const zones = status.zones ?? [];
  const visible = status.zones_visible ?? zones.length;
  const shown = zones.slice(0, ZONES_SHOWN).join(', ');
  return (
    <DescriptionList
      items={[
        {
          label: t('common.status'),
          value: <Badge tone="success">{t('domains.dnsProvider.active')}</Badge>,
        },
        {
          label: t('domains.dnsProvider.field.token'),
          value: status.token_hint
            ? t('domains.dnsProvider.tokenHint', { hint: status.token_hint })
            : t('common.dash'),
        },
        {
          label: t('domains.dnsProvider.field.zones'),
          value:
            visible > ZONES_SHOWN
              ? t('domains.dnsProvider.zonesMore', { shown, count: visible - ZONES_SHOWN })
              : shown || t('common.dash'),
        },
        {
          label: t('domains.dnsProvider.field.connectedBy'),
          value: status.connected_by ? (
            <span className="cf-mono">{status.connected_by}</span>
          ) : (
            t('common.dash')
          ),
        },
        {
          label: t('domains.dnsProvider.field.connectedAt'),
          value: formatDateTime(status.connected_at ?? null),
        },
        {
          label: t('domains.dnsProvider.field.lastValidated'),
          value: formatDateTime(status.last_validated_at ?? null),
        },
      ]}
    />
  );
}

const CONNECT_FORM_ID = 'dns-provider-connect-form';

function ConnectForm({
  onClose,
  onConnected,
}: {
  onClose: () => void;
  onConnected: (status: DnsProviderStatus) => void;
}) {
  const [token, setToken] = useState('');

  const action = useAction(async () => {
    try {
      const { data } = await dnsProvidersApi.connect(DNS_PROVIDER_CLOUDFLARE, token.trim());
      onConnected(data);
    } finally {
      setToken('');
    }
  });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    await action.run();
  };

  const close = () => {
    setToken('');
    onClose();
  };

  return (
    <Modal
      open
      title={t('domains.dnsProvider.form.title')}
      onClose={close}
      footer={
        <>
          <Button onClick={close} disabled={action.busy}>
            {t('common.cancel')}
          </Button>
          <Button
            type="submit"
            form={CONNECT_FORM_ID}
            variant="primary"
            loading={action.busy}
            disabled={!token.trim()}
          >
            {t('domains.dnsProvider.form.submit')}
          </Button>
        </>
      }
    >
      <form id={CONNECT_FORM_ID} className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
        <FormField
          label={t('domains.dnsProvider.form.token')}
          htmlFor="dns-provider-token"
          hint={t('domains.dnsProvider.form.tokenHint')}
          required
        >
          <Input
            id="dns-provider-token"
            type="password"
            autoComplete="off"
            spellCheck={false}
            value={token}
            onChange={(e) => setToken(e.target.value)}
          />
        </FormField>
        {action.error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(action.error)}
          </div>
        ) : null}
      </form>
    </Modal>
  );
}
