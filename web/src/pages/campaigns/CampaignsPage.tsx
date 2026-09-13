import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  CAMPAIGN_STATUSES,
  campaignsApi,
  type Campaign,
  type CampaignStatus,
} from '@/api/campaigns';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import {
  Button,
  Card,
  DataTable,
  Input,
  PageHeader,
  Select,
  useToast,
  type Column,
} from '@/design/components';
import { IconPlus } from '@/design/icons';
import { formatRate } from '@/lib/decimal';
import { formatDateTime } from '@/lib/format';
import { getLocale, t, tEnum } from '@/i18n';
import { paths } from '@/paths';
import { CampaignForm } from './CampaignForm';
import { CampaignStatusBadge } from './campaignStatus';

const SEARCH_DEBOUNCE_MS = 300;

export default function CampaignsPage() {
  const navigate = useNavigate();
  const toast = useToast();
  const { can } = useAccess();
  const pager = usePagination();
  const [status, setStatus] = useState<CampaignStatus | ''>('');
  const [searchInput, setSearchInput] = useState('');
  const [search, setSearch] = useState('');
  const [creating, setCreating] = useState(false);
  const format = new Intl.NumberFormat(getLocale());

  useEffect(() => {
    const handle = window.setTimeout(() => {
      setSearch(searchInput.trim());
      pager.reset();
    }, SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(handle);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [searchInput]);

  const campaigns = useQuery(
    () =>
      campaignsApi.list({
        page: pager.page,
        per_page: pager.perPage,
        status: status || undefined,
        search: search || undefined,
      }),
    [pager.page, pager.perPage, status, search],
  );

  const columns: Column<Campaign>[] = [
    {
      key: 'name',
      header: t('common.name'),
      render: (c) => (
        <div className="cf-cell-stack">
          <strong>{c.name}</strong>
          {c.description ? <span className="cf-text-muted cf-text-sm">{c.description}</span> : null}
        </div>
      ),
    },
    {
      key: 'status',
      header: t('common.status'),
      render: (c) => <CampaignStatusBadge status={c.status} />,
    },
    {
      key: 'when',
      header: t('campaigns.column.when'),
      render: (c) =>
        c.started_at
          ? t('campaigns.startedAt', { date: formatDateTime(c.started_at) })
          : c.scheduled_at
            ? t('campaigns.scheduledFor', { date: formatDateTime(c.scheduled_at) })
            : t('common.dash'),
    },
    {
      key: 'sent',
      header: t('campaigns.stats.sent'),
      align: 'right',
      render: (c) => (c.stats ? format.format(c.stats.sent) : t('common.dash')),
    },
    {
      key: 'open',
      header: t('campaigns.rates.open_rate'),
      align: 'right',
      render: (c) => (c.stats ? formatRate(c.stats.rates.open_rate) : t('common.dash')),
    },
    { key: 'updated', header: t('common.updatedAt'), render: (c) => formatDateTime(c.updated_at) },
  ];

  return (
    <div>
      <PageHeader
        title={t('campaigns.title')}
        description={t('campaigns.subtitle')}
        actions={
          can(...PERMISSIONS.campaigns.create) ? (
            <Button
              variant="primary"
              icon={<IconPlus size={16} />}
              onClick={() => setCreating(true)}
            >
              {t('campaigns.new')}
            </Button>
          ) : null
        }
      />
      <Card flush>
        <div className="cf-toolbar">
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="campaigns-search">
              {t('common.search')}
            </label>
            <Input
              id="campaigns-search"
              type="search"
              placeholder={t('campaigns.searchPlaceholder')}
              value={searchInput}
              onChange={(e) => setSearchInput(e.target.value)}
            />
          </div>
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="campaigns-status">
              {t('common.status')}
            </label>
            <Select
              id="campaigns-status"
              placeholder={t('common.all')}
              options={CAMPAIGN_STATUSES.map((s) => ({
                value: s,
                label: tEnum('campaigns.status', s),
              }))}
              value={status}
              onChange={(e) => {
                setStatus(e.target.value as CampaignStatus | '');
                pager.reset();
              }}
            />
          </div>
        </div>
        <DataTable
          columns={columns}
          rows={campaigns.data?.items ?? []}
          rowKey={(c) => c.id}
          loading={campaigns.loading}
          error={campaigns.error}
          onRetry={campaigns.reload}
          empty={{ title: t('campaigns.empty'), description: t('campaigns.emptyDescription') }}
          onRowClick={(c) => navigate(paths.campaign(c.id))}
          pagination={{
            page: campaigns.data?.page ?? pager.page,
            perPage: pager.perPage,
            total: campaigns.data?.total ?? 0,
            totalPages: campaigns.data?.totalPages ?? 0,
            onPageChange: pager.setPage,
          }}
        />
      </Card>
      {creating ? (
        <CampaignForm
          campaign={null}
          onClose={() => setCreating(false)}
          onSaved={(created) => {
            toast.success(t('campaigns.created'));
            setCreating(false);
            navigate(paths.campaign(created.id));
          }}
        />
      ) : null}
    </div>
  );
}
