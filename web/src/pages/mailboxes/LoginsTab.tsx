import { mailDirectoryApi, type Mailbox, type SaslLogin } from '@/api/mailDirectory';
import { useQuery } from '@/hooks/useQuery';
import { Badge, Button, Card, DataTable, type Column } from '@/design/components';
import { IconRefresh } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { t, tEnum } from '@/i18n';

// Cuantos inicios de sesion se piden; el API acepta hasta 500.
const LOGIN_LIMIT = 100;

export function LoginsTab({ mailbox }: { mailbox: Mailbox }) {
  const logins = useQuery(
    () => mailDirectoryApi.mailboxLogins(mailbox.id, LOGIN_LIMIT),
    [mailbox.id],
  );

  const columns: Column<SaslLogin>[] = [
    { key: 'when', header: t('logins.column.when'), render: (l) => formatDateTime(l.logged_at) },
    {
      key: 'service',
      header: t('logins.column.service'),
      render: (l) => <Badge tone="info">{tEnum('logins.service', l.service)}</Badge>,
    },
    {
      key: 'ip',
      header: t('logins.column.ip'),
      render: (l) => <span className="cf-mono">{l.remote_ip}</span>,
    },
    {
      key: 'credential',
      header: t('logins.column.credential'),
      render: (l) =>
        l.app_password_id ? (
          <Badge tone="accent">{t('logins.appPassword')}</Badge>
        ) : (
          t('logins.mainPassword')
        ),
    },
  ];

  return (
    <Card
      flush
      title={t('logins.title')}
      description={t('logins.description', { n: LOGIN_LIMIT })}
      actions={
        <Button
          size="sm"
          icon={<IconRefresh size={14} />}
          onClick={logins.reload}
          loading={logins.loading}
        >
          {t('common.refresh')}
        </Button>
      }
    >
      <DataTable
        columns={columns}
        rows={logins.data ?? []}
        rowKey={(l) => l.id}
        loading={logins.loading}
        error={logins.error}
        onRetry={logins.reload}
        empty={{ title: t('logins.empty') }}
      />
    </Card>
  );
}
