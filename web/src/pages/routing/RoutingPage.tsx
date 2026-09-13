import type { ComponentType } from 'react';
import { PERMISSIONS, type CrudPermissions } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useTabParam } from '@/hooks/useTabParam';
import { Card, EmptyState, PageHeader, Tabs } from '@/design/components';
import { t, type MessageKey } from '@/i18n';
import { AliasesTab } from './AliasesTab';
import { BccMapsTab } from './BccMapsTab';
import { RecipientMapsTab } from './RecipientMapsTab';
import { RelayhostsTab } from './RelayhostsTab';
import { SenderAclTab } from './SenderAclTab';
import { SpamAliasesTab } from './SpamAliasesTab';
import { TlsPoliciesTab } from './TlsPoliciesTab';
import { TransportsTab } from './TransportsTab';

type TabId =
  | 'aliases'
  | 'spamAliases'
  | 'senderAcl'
  | 'relayhosts'
  | 'transports'
  | 'tlsPolicies'
  | 'recipientMaps'
  | 'bccMaps';

interface RoutingTab {
  id: TabId;
  label: MessageKey;
  permissions: CrudPermissions;
  Component: ComponentType;
}

// Una pestana por recurso de mail-directory/mail-routing, cada una con su permiso de lectura.
const TABS: readonly RoutingTab[] = [
  {
    id: 'aliases',
    label: 'routing.tab.aliases',
    permissions: PERMISSIONS.aliases,
    Component: AliasesTab,
  },
  {
    id: 'spamAliases',
    label: 'routing.tab.spamAliases',
    permissions: PERMISSIONS.spamAliases,
    Component: SpamAliasesTab,
  },
  {
    id: 'senderAcl',
    label: 'routing.tab.senderAcl',
    permissions: PERMISSIONS.senderAcl,
    Component: SenderAclTab,
  },
  {
    id: 'relayhosts',
    label: 'routing.tab.relayhosts',
    permissions: PERMISSIONS.relayhosts,
    Component: RelayhostsTab,
  },
  {
    id: 'transports',
    label: 'routing.tab.transports',
    permissions: PERMISSIONS.transports,
    Component: TransportsTab,
  },
  {
    id: 'tlsPolicies',
    label: 'routing.tab.tlsPolicies',
    permissions: PERMISSIONS.tlsPolicies,
    Component: TlsPoliciesTab,
  },
  {
    id: 'recipientMaps',
    label: 'routing.tab.recipientMaps',
    permissions: PERMISSIONS.recipientMaps,
    Component: RecipientMapsTab,
  },
  {
    id: 'bccMaps',
    label: 'routing.tab.bccMaps',
    permissions: PERMISSIONS.bccMaps,
    Component: BccMapsTab,
  },
];

export default function RoutingPage() {
  const { can } = useAccess();
  const visible = TABS.filter((tab) => can(...tab.permissions.read));
  const [tab, setTab] = useTabParam(
    visible.map((item) => item.id),
    'aliases',
  );
  const active = visible.find((item) => item.id === tab);

  return (
    <div>
      <PageHeader title={t('routing.title')} description={t('routing.subtitle')} />
      {active ? (
        <>
          <Tabs
            items={visible.map((item) => ({ id: item.id, label: t(item.label) }))}
            value={tab}
            onChange={setTab}
            label={t('routing.title')}
          />
          <active.Component />
        </>
      ) : (
        <Card>
          <EmptyState title={t('routing.noAccess')} />
        </Card>
      )}
    </div>
  );
}
