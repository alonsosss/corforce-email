import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { directoryMeta, mailDirectoryApi, type Mailbox } from '@/api/mailDirectory';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import { useResource } from '@/hooks/useResource';
import {
  Badge,
  Button,
  Card,
  DataTable,
  Input,
  PageHeader,
  useToast,
  type Column,
} from '@/design/components';
import { IconPlus, IconSearch } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { formatQuota } from '@/lib/quota';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { DirectoryDomainPicker } from '@/pages/shared/DirectoryDomainPicker';
import { ActiveStateBadge } from '@/pages/shared/StatusBadges';
import { ProtocolBadges } from './access';
import { AssistantSettingsCard } from './AssistantSettingsCard';
import { MailboxCreateForm } from './MailboxCreateForm';

const SEARCH_DEBOUNCE_MS = 300;

/** Buzones de la celda con busqueda y filtro por dominio en el servidor (bajo RLS). */
export default function MailboxesPage() {
  const navigate = useNavigate();
  const toast = useToast();
  const { can } = useAccess();
  const meta = useResource(directoryMeta);
  const pager = usePagination();
  const [searchInput, setSearchInput] = useState('');
  const [search, setSearch] = useState('');
  const [domain, setDomain] = useState('');
  const [creating, setCreating] = useState(false);

  useEffect(() => {
    const handle = window.setTimeout(() => {
      setSearch(searchInput.trim());
      pager.reset();
    }, SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(handle);
    // pager.reset es estable; solo el texto escrito dispara la busqueda.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [searchInput]);

  const mailboxes = useQuery(
    () =>
      mailDirectoryApi.listMailboxes({
        page: pager.page,
        per_page: pager.perPage,
        search: search || undefined,
        domain: domain.trim() || undefined,
      }),
    [pager.page, pager.perPage, search, domain],
  );

  const filtering = Boolean(search || domain.trim());

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
                type="search"
                placeholder={t('mailboxes.searchPlaceholder')}
                value={searchInput}
                maxLength={meta.data?.search.max_length}
                onChange={(e) => setSearchInput(e.target.value)}
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
            <DirectoryDomainPicker
              id="mailboxes-domain"
              placeholder={t('common.all')}
              value={domain}
              onChange={(next) => {
                setDomain(next);
                pager.reset();
              }}
            />
          </div>
        </div>
        <DataTable
          columns={columns}
          rows={mailboxes.data?.items ?? []}
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
      {can(...PERMISSIONS.assistantSettings.read) ? (
        <div style={{ marginTop: 'var(--cf-space-6)' }}>
          <AssistantSettingsCard />
        </div>
      ) : null}
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
