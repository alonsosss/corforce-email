import type { ComponentType } from 'react';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useTabParam } from '@/hooks/useTabParam';
import { Card, EmptyState, PageHeader, Tabs } from '@/design/components';
import { t, type MessageKey } from '@/i18n';
import { AddressListsTab } from './AddressListsTab';
import { FootersTab } from './FootersTab';
import { ForwardingHostsTab } from './ForwardingHostsTab';
import { MailboxTagsTab } from './MailboxTagsTab';
import { QuarantineSettingsTab } from './QuarantineSettingsTab';
import { RateLimitsTab } from './RateLimitsTab';
import { SpamScoresTab } from './SpamScoresTab';

type TabId =
  | 'spamScores'
  | 'addressLists'
  | 'footers'
  | 'forwardingHosts'
  | 'rateLimits'
  | 'mailboxTags'
  | 'quarantineSettings';

interface SecurityTab {
  id: TabId;
  label: MessageKey;
  read: readonly [string, string, string];
  Component: ComponentType;
}

const TABS: readonly SecurityTab[] = [
  {
    id: 'spamScores',
    label: 'security.tab.spamScores',
    read: PERMISSIONS.spamScores.read,
    Component: SpamScoresTab,
  },
  {
    id: 'addressLists',
    label: 'security.tab.addressLists',
    read: PERMISSIONS.addressLists.read,
    Component: AddressListsTab,
  },
  {
    id: 'footers',
    label: 'security.tab.footers',
    read: PERMISSIONS.footers.read,
    Component: FootersTab,
  },
  {
    id: 'forwardingHosts',
    label: 'security.tab.forwardingHosts',
    read: PERMISSIONS.forwardingHosts.read,
    Component: ForwardingHostsTab,
  },
  {
    id: 'rateLimits',
    label: 'security.tab.rateLimits',
    read: PERMISSIONS.rateLimits.read,
    Component: RateLimitsTab,
  },
  {
    id: 'mailboxTags',
    label: 'security.tab.mailboxTags',
    read: PERMISSIONS.mailboxTags.read,
    Component: MailboxTagsTab,
  },
  {
    id: 'quarantineSettings',
    label: 'security.tab.quarantineSettings',
    read: PERMISSIONS.quarantineSettings.read,
    Component: QuarantineSettingsTab,
  },
];

export default function MailSecurityPage() {
  const { can } = useAccess();
  const visible = TABS.filter((tab) => can(...tab.read));
  const [tab, setTab] = useTabParam(
    visible.map((item) => item.id),
    'spamScores',
  );
  const active = visible.find((item) => item.id === tab);

  return (
    <div>
      <PageHeader title={t('security.title')} description={t('security.subtitle')} />
      {active ? (
        <>
          <Tabs
            items={visible.map((item) => ({ id: item.id, label: t(item.label) }))}
            value={tab}
            onChange={setTab}
            label={t('security.title')}
          />
          <active.Component />
        </>
      ) : (
        <Card>
          <EmptyState title={t('security.noAccess')} />
        </Card>
      )}
    </div>
  );
}
