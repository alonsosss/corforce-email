import { useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { DIRECTORY_MAX_PAGE_SIZE, mailDirectoryApi, type Mailbox } from '@/api/mailDirectory';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import {
  Badge,
  Button,
  Card,
  DataTable,
  Input,
  PageHeader,
  Select,
  useToast,
  type Column,
} from '@/design/components';
import { IconPlus, IconSearch } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { formatQuota } from '@/lib/quota';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { ActiveStateBadge } from '@/pages/shared/StatusBadges';
import { ProtocolBadges } from './access';
import { MailboxCreateForm } from './MailboxCreateForm';

/**
 * El listado de mail-directory aun no filtra en servidor: se pide la pagina mas grande que
 * admite el API y la busqueda y el filtro por dominio se aplican sobre esa pagina.
 */
export function filterMailboxes(items: Mailbox[], search: string, domain: string): Mailbox[] {
  const needle = search.trim().toLowerCase();
  return items.filter(
    (m) =>
      (!domain || m.domain === domain) &&
      (!needle ||
        m.username.toLowerCase().includes(needle) ||
        m.display_name.toLowerCase().includes(needle)),
  );
}

export default function MailboxesPage() {
  const navigate = useNavigate();
  const toast = useToast();
  const { can } = useAccess();
  const pager = usePagination(DIRECTORY_MAX_PAGE_SIZE);
  const [search, setSearch] = useState('');
  const [domain, setDomain] = useState('');
  const [creating, setCreating] = useState(false);

  const mailboxes = useQuery(
    () => mailDirectoryApi.listMailboxes({ page: pager.page, per_page: pager.perPage }),
    [pager.page, pager.perPage],
  );

  const items = mailboxes.data?.items;
  const rows = useMemo(() => filterMailboxes(items ?? [], search, domain), [items, search, domain]);
  const domainOptions = useMemo(
    () =>
      [...new Set((items ?? []).map((m) => m.domain))].sort().map((d) => ({ value: d, label: d })),
    [items],
  );
  const filtering = Boolean(search.trim() || domain);

  const columns: Column<Mailbox>[] = [
    {
      key: 'address',
      header: t('mailboxes.column.address'),
      render: (m) => (
        <div className="cf-cell-stack">
          <strong className="cf-mono">{m.username}</strong>
          {m.display_name ? (
            <span className="cf-text-muted cf-text-sm">{m.display_name}</span>
          ) : null}
        </div>
      ),
    },
    {
      key: 'status',
      header: t('common.status'),
      render: (m) => <ActiveStateBadge state={m.active} />,
    },
    {
      key: 'quota',
      header: t('mailboxes.column.quota'),
      align: 'right',
      render: (m) => formatQuota(m.quota_bytes),
    },
    {
      key: 'protocols',
      header: t('mailboxes.column.protocols'),
      render: (m) => <ProtocolBadges value={m} />,
    },
    {
      key: 'tls',
      header: t('mailboxes.column.tls'),
      render: (m) => (
        <span className="cf-inline-list">
          {m.tls_enforce_in ? <Badge>{t('mailboxes.tlsIn')}</Badge> : null}
          {m.tls_enforce_out ? <Badge>{t('mailboxes.tlsOut')}</Badge> : null}
        </span>
      ),
    },
    { key: 'updated', header: t('common.updatedAt'), render: (m) => formatDateTime(m.updated_at) },
  ];

  return (
    <div>
      <PageHeader
        title={t('mailboxes.title')}
        description={t('mailboxes.subtitle')}
        actions={
          can(...PERMISSIONS.mailboxes.create) ? (
            <Button
              variant="primary"
              icon={<IconPlus size={16} />}
              onClick={() => setCreating(true)}
            >
              {t('mailboxes.new')}
            </Button>
          ) : null
        }
      />
      <Card flush>
        <div className="cf-toolbar">
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="mailboxes-search">
              {t('common.search')}
            </label>
            <div className="cf-input-group">
              <Input
                id="mailboxes-search"
                placeholder={t('mailboxes.searchPlaceholder')}
                value={search}
                onChange={(e) => setSearch(e.target.value)}
              />
              <span className="cf-input-group__addon" style={{ right: 10 }}>
                <IconSearch size={16} />
              </span>
            </div>
          </div>
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="mailboxes-domain">
              {t('mailboxes.filter.domain')}
            </label>
            <Select
              id="mailboxes-domain"
              placeholder={t('common.all')}
              options={domainOptions}
              value={domain}
              onChange={(e) => setDomain(e.target.value)}
            />
          </div>
        </div>
        {filtering && (mailboxes.data?.totalPages ?? 0) > 1 ? (
          <div className="cf-toolbar cf-text-sm cf-text-secondary">
            {t('mailboxes.filterScope')}
          </div>
        ) : null}
        <DataTable
          columns={columns}
          rows={rows}
          rowKey={(m) => m.id}
          loading={mailboxes.loading}
          error={mailboxes.error}
          onRetry={mailboxes.reload}
          empty={{
            title: filtering ? t('mailboxes.noMatches') : t('mailboxes.empty'),
            description: filtering ? undefined : t('mailboxes.emptyDescription'),
          }}
          onRowClick={(m) => navigate(paths.mailbox(m.id))}
          pagination={{
            page: mailboxes.data?.page ?? pager.page,
            perPage: pager.perPage,
            total: mailboxes.data?.total ?? 0,
            totalPages: mailboxes.data?.totalPages ?? 0,
            onPageChange: pager.setPage,
          }}
        />
      </Card>
      {creating ? (
        <MailboxCreateForm
          onClose={() => setCreating(false)}
          onCreated={(mailbox) => {
            toast.success(t('mailboxes.created'));
            setCreating(false);
            navigate(paths.mailbox(mailbox.id));
          }}
        />
      ) : null}
    </div>
  );
}
