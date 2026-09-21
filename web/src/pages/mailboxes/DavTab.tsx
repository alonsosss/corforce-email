import { directoryMeta, type DirectoryMeta, type Mailbox } from '@/api/mailDirectory';
import { useResource } from '@/hooks/useResource';
import { Alert, Button, Card, CopyButton, DescriptionList } from '@/design/components';
import { t } from '@/i18n';
import { ResourceGate } from '@/pages/shared/ResourceGate';

export interface DavTabProps {
  mailbox: Mailbox;
  /** Ausente cuando el usuario no puede ver las contrasenas de aplicacion. */
  onOpenAppPasswords?: () => void;
}

export function DavTab(props: DavTabProps) {
  const meta = useResource(directoryMeta);
  return (
    <ResourceGate resource={meta}>
      {(data) => <DavConnection {...props} meta={data} />}
    </ResourceGate>
  );
}

function DavConnection({
  mailbox,
  meta,
  onOpenAppPasswords,
}: DavTabProps & { meta: DirectoryMeta }) {
  const server = meta.dav;
  return (
    <Card title={t('dav.title')} description={t('dav.description')}>
      <div className="cf-stack">
        {!mailbox.dav_access ? (
          <Alert tone="warning" title={t('dav.disabled.title')}>
            {t('dav.disabled.body')}
          </Alert>
        ) : null}
        {server ? (
          <>
            <DescriptionList
              items={[
                {
                  label: t('dav.serverUrl'),
                  value: (
                    <span className="cf-inline">
                      <span className="cf-mono">{server.server_url}</span>
                      <CopyButton value={server.server_url} />
                    </span>
                  ),
                },
                { label: t('dav.discovery'), value: t('dav.discoveryHint') },
                {
                  label: t('dav.username'),
                  value: (
                    <span className="cf-inline">
                      <span className="cf-mono">{mailbox.username}</span>
                      <CopyButton value={mailbox.username} />
                    </span>
                  ),
                },
                { label: t('dav.password'), value: t('dav.passwordHint') },
              ]}
            />
            {onOpenAppPasswords ? (
              <div>
                <Button size="sm" onClick={onOpenAppPasswords}>
                  {t('dav.openAppPasswords')}
                </Button>
              </div>
            ) : null}
          </>
        ) : (
          <Alert tone="warning" title={t('dav.notConfigured.title')}>
            {t('dav.notConfigured.body')}
          </Alert>
        )}
      </div>
    </Card>
  );
}
