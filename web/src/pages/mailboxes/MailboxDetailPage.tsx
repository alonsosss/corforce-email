import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { mailDirectoryApi } from '@/api/mailDirectory';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useQuery } from '@/hooks/useQuery';
import { useTabParam } from '@/hooks/useTabParam';
import {
  Button,
  Card,
  ConfirmDialog,
  ErrorState,
  PageHeader,
  Skeleton,
  Tabs,
  useToast,
} from '@/design/components';
import { IconTrash } from '@/design/icons';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { ActiveStateBadge } from '@/pages/shared/StatusBadges';
import { AppPasswordsTab } from './AppPasswordsTab';
import { LoginsTab } from './LoginsTab';
import { MailboxDataTab } from './MailboxDataTab';
import { MailboxPasswordTab } from './MailboxPasswordTab';
import { MailboxQuotaTab } from './MailboxQuotaTab';
import { SieveTab } from './SieveTab';
import { VacationTab } from './VacationTab';

type TabId = 'data' | 'password' | 'quota' | 'appPasswords' | 'sieve' | 'vacation' | 'logins';

export default function MailboxDetailPage() {
  const { id = '' } = useParams();
  const navigate = useNavigate();
  const toast = useToast();
  const { can } = useAccess();
  const [deleting, setDeleting] = useState(false);
  const mailbox = useQuery(async () => (await mailDirectoryApi.getMailbox(id)).data, [id]);

  const tabs: { id: TabId; label: string }[] = [
    { id: 'data', label: t('mailboxes.tab.data') },
    ...(can(...PERMISSIONS.mailboxes.setPassword)
      ? [{ id: 'password' as const, label: t('mailboxes.tab.password') }]
      : []),
    { id: 'quota', label: t('mailboxes.tab.quota') },
    ...(can(...PERMISSIONS.appPasswords.read)
      ? [{ id: 'appPasswords' as const, label: t('mailboxes.tab.appPasswords') }]
      : []),
    ...(can(...PERMISSIONS.sieve.read)
      ? [
          { id: 'sieve' as const, label: t('mailboxes.tab.sieve') },
          { id: 'vacation' as const, label: t('mailboxes.tab.vacation') },
        ]
      : []),
    { id: 'logins', label: t('mailboxes.tab.logins') },
  ];
  const [tab, setTab] = useTabParam(
    tabs.map((item) => item.id),
    'data',
  );

  if (mailbox.error) {
    return (
      <div>
        <PageHeader
          title={t('mailboxes.title')}
          back={{ to: paths.mailboxes, label: t('nav.mailboxes') }}
        />
        <Card>
          <ErrorState
            error={mailbox.error}
            title={t('mailboxes.notFound')}
            onRetry={mailbox.reload}
          />
        </Card>
      </div>
    );
  }
  if (!mailbox.data) {
    return (
      <Card>
        <Skeleton lines={6} />
      </Card>
    );
  }

  const m = mailbox.data;

  return (
    <div>
      <PageHeader
        title={m.username}
        description={
          <span className="cf-inline">
            <ActiveStateBadge state={m.active} />
            {m.display_name ? <span>{m.display_name}</span> : null}
          </span>
        }
        back={{ to: paths.mailboxes, label: t('nav.mailboxes') }}
        actions={
          can(...PERMISSIONS.mailboxes.delete) ? (
            <Button
              variant="danger"
              icon={<IconTrash size={16} />}
              onClick={() => setDeleting(true)}
            >
              {t('common.delete')}
            </Button>
          ) : null
        }
      />
      <Tabs items={tabs} value={tab} onChange={setTab} label={m.username} />
      {tab === 'data' ? <MailboxDataTab mailbox={m} onChange={mailbox.setData} /> : null}
      {tab === 'password' ? <MailboxPasswordTab mailbox={m} /> : null}
      {tab === 'quota' ? <MailboxQuotaTab mailbox={m} /> : null}
      {tab === 'appPasswords' ? <AppPasswordsTab mailbox={m} /> : null}
      {tab === 'sieve' ? <SieveTab mailbox={m} /> : null}
      {tab === 'vacation' ? <VacationTab mailbox={m} /> : null}
      {tab === 'logins' ? <LoginsTab mailbox={m} /> : null}
      <ConfirmDialog
        open={deleting}
        title={t('mailboxes.delete')}
        message={t('mailboxes.deleteConfirm', { address: m.username })}
        confirmLabel={t('common.delete')}
        danger
        onCancel={() => setDeleting(false)}
        onConfirm={async () => {
          await mailDirectoryApi.deleteMailbox(m.id);
          toast.success(t('mailboxes.deleted'));
          navigate(paths.mailboxes, { replace: true });
        }}
      />
    </div>
  );
}
