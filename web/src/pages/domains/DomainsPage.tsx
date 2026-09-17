import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { domainsApi, type DomainWithRecords, type ManagedDomain } from '@/api/domains';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import { useTabParam } from '@/hooks/useTabParam';
import {
  Alert,
  Badge,
  Button,
  Card,
  DataTable,
  Modal,
  PageHeader,
  Tabs,
  useToast,
  type Column,
} from '@/design/components';
import { IconPlus } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { t, tEnum } from '@/i18n';
import { paths } from '@/paths';
import { AliasDomainsTab } from './AliasDomainsTab';
import { DirectoryTab } from './DirectoryTab';
import { DnsProviderCard } from './DnsProviderCard';
import { DnsRecordsTable } from './DnsRecordsTable';
import { DomainCreateForm } from './DomainCreateForm';
import { domainStatusTone } from './domainStatus';

type TabId = 'domains' | 'directory' | 'aliasDomains';

export default function DomainsPage() {
  const { can } = useAccess();
  const tabs: { id: TabId; label: string }[] = [
    { id: 'domains', label: t('domains.tab.domains') },
    { id: 'directory', label: t('domains.tab.directory') },
  ];
  if (can(...PERMISSIONS.aliasDomains.read)) {
    tabs.push({ id: 'aliasDomains', label: t('domains.tab.aliasDomains') });
  }
  const [tab, setTab] = useTabParam(
    tabs.map((item) => item.id),
    'domains',
  );

  return (
    <div>
      <PageHeader title={t('domains.title')} description={t('domains.subtitle')} />
      <Tabs items={tabs} value={tab} onChange={setTab} label={t('domains.title')} />
      {tab === 'domains' ? (
        <div className="cf-stack">
          <DomainsList />
          {can(...PERMISSIONS.dnsProviders.read) ? <DnsProviderCard /> : null}
        </div>
      ) : null}
      {tab === 'directory' ? <DirectoryTab /> : null}
      {tab === 'aliasDomains' ? <AliasDomainsTab /> : null}
    </div>
  );
}

function DomainsList() {
  const navigate = useNavigate();
  const toast = useToast();
  const { can } = useAccess();
  const pager = usePagination();
  const [creating, setCreating] = useState(false);
  const [created, setCreated] = useState<DomainWithRecords | null>(null);

  const domains = useQuery(
    () => domainsApi.list({ page: pager.page, per_page: pager.perPage }),
    [pager.page, pager.perPage],
  );

  const columns: Column<ManagedDomain>[] = [
    {
      key: 'domain',
      header: t('domains.column.domain'),
      render: (d) => <strong className="cf-mono">{d.domain}</strong>,
    },
    {
      key: 'purpose',
      header: t('domains.column.purpose'),
      render: (d) => tEnum('domains.purpose', d.purpose),
    },
    {
      key: 'status',
      header: t('common.status'),
      render: (d) => (
        <Badge tone={domainStatusTone(d.status)}>{tEnum('domains.status', d.status)}</Badge>
      ),
    },
    {
      key: 'dmarc',
      header: t('domains.column.dmarc'),
      render: (d) => tEnum('domains.dmarc', d.dmarc_policy),
    },
    {
      key: 'checked',
      header: t('domains.column.lastChecked'),
      render: (d) => formatDateTime(d.last_checked_at),
    },
    {
      key: 'verified',
      header: t('domains.column.verifiedAt'),
      render: (d) => formatDateTime(d.verified_at),
    },
  ];

  return (
    <Card
      flush
      actions={
        can(...PERMISSIONS.domains.create) ? (
          <Button variant="primary" icon={<IconPlus size={16} />} onClick={() => setCreating(true)}>
            {t('domains.new')}
          </Button>
        ) : null
      }
      title={t('domains.listTitle')}
      description={t('domains.listDescription')}
    >
      <DataTable
        columns={columns}
        rows={domains.data?.items ?? []}
        rowKey={(d) => d.id}
        loading={domains.loading}
        error={domains.error}
        onRetry={domains.reload}
        empty={{ title: t('domains.empty'), description: t('domains.emptyDescription') }}
        onRowClick={(d) => navigate(paths.domain(d.id))}
        pagination={{
          page: domains.data?.page ?? pager.page,
          perPage: pager.perPage,
          total: domains.data?.total ?? 0,
          totalPages: domains.data?.totalPages ?? 0,
          onPageChange: pager.setPage,
        }}
      />
      {creating ? (
        <DomainCreateForm
          onClose={() => setCreating(false)}
          onCreated={(domain) => {
            toast.success(t('domains.created'));
            setCreating(false);
            setCreated(domain);
            domains.reload();
          }}
        />
      ) : null}
      {created ? (
        <Modal
          open
          size="lg"
          title={t('domains.publishTitle', { domain: created.domain })}
          onClose={() => setCreated(null)}
          footer={
            <>
              <Button onClick={() => setCreated(null)}>{t('common.close')}</Button>
              <Button variant="primary" onClick={() => navigate(paths.domain(created.id))}>
                {t('domains.openDetail')}
              </Button>
            </>
          }
        >
          <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
            <Alert tone="info">{t('domains.publishDescription')}</Alert>
            <DnsRecordsTable records={created.dns_records} />
          </div>
        </Modal>
      ) : null}
    </Card>
  );
}
