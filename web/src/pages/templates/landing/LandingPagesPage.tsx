import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { pagesApi, type LandingPage, type LandingPageStatus } from '@/api/pages';
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
import { IconPlus } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { t, tEnum } from '@/i18n';
import { paths } from '@/paths';
import { PageCreateForm } from './PageCreateForm';
import { PublicUrl } from './PublicUrl';
import { isPublished, pageStatusTone } from './pageStatus';

const SEARCH_DEBOUNCE_MS = 300;
const STATUSES: readonly LandingPageStatus[] = ['active', 'archived'];

export default function LandingPagesPage() {
  const navigate = useNavigate();
  const toast = useToast();
  const { can } = useAccess();
  const pager = usePagination();
  const [status, setStatus] = useState<LandingPageStatus | ''>('');
  const [searchInput, setSearchInput] = useState('');
  const [search, setSearch] = useState('');
  const [creating, setCreating] = useState(false);

  useEffect(() => {
    const handle = window.setTimeout(() => {
      setSearch(searchInput.trim());
      pager.reset();
    }, SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(handle);
    // pager.reset es estable (useCallback); solo el texto dispara la busqueda.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [searchInput]);

  const pages = useQuery(
    () =>
      pagesApi.list({
        page: pager.page,
        per_page: pager.perPage,
        status: status || undefined,
        search: search || undefined,
      }),
    [pager.page, pager.perPage, status, search],
  );

  const columns: Column<LandingPage>[] = [
    {
      key: 'name',
      header: t('common.name'),
      render: (p) => (
        <div className="cf-cell-stack">
          <strong>{p.name}</strong>
          <span className="cf-text-muted cf-text-sm cf-mono">{p.slug}</span>
        </div>
      ),
    },
    {
      key: 'status',
      header: t('common.status'),
      render: (p) => (
        <Badge tone={pageStatusTone(p.status)}>{tEnum('templates.pages.status', p.status)}</Badge>
      ),
    },
    {
      key: 'version',
      header: t('templates.column.published'),
      render: (p) =>
        isPublished(p) ? (
          t('templates.versionLabel', { n: p.current_version })
        ) : (
          <Badge tone="warning">{t('templates.pages.unpublished')}</Badge>
        ),
    },
    {
      key: 'url',
      header: t('templates.pages.publicUrl'),
      render: (p) => <PublicUrl page={p} />,
    },
    { key: 'updated', header: t('common.updatedAt'), render: (p) => formatDateTime(p.updated_at) },
  ];

  return (
    <div>
      <PageHeader
        title={t('templates.pages.title')}
        description={t('templates.pages.subtitle')}
        actions={
          can(...PERMISSIONS.landingPages.create) ? (
            <Button
              variant="primary"
              icon={<IconPlus size={16} />}
              onClick={() => setCreating(true)}
            >
              {t('templates.pages.new')}
            </Button>
          ) : null
        }
      />
      <Card flush>
        <div className="cf-toolbar">
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="pages-search">
              {t('common.search')}
            </label>
            <Input
              id="pages-search"
              placeholder={t('templates.pages.searchPlaceholder')}
              value={searchInput}
              onChange={(e) => setSearchInput(e.target.value)}
            />
          </div>
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="pages-status">
              {t('common.status')}
            </label>
            <Select
              id="pages-status"
              placeholder={t('common.all')}
              options={STATUSES.map((s) => ({
                value: s,
                label: tEnum('templates.pages.status', s),
              }))}
              value={status}
              onChange={(e) => {
                setStatus(e.target.value as LandingPageStatus | '');
                pager.reset();
              }}
            />
          </div>
        </div>
        <DataTable
          columns={columns}
          rows={pages.data?.items ?? []}
          rowKey={(p) => p.id}
          loading={pages.loading}
          error={pages.error}
          onRetry={pages.reload}
          empty={{
            title: t('templates.pages.empty'),
            description: t('templates.pages.emptyDescription'),
          }}
          onRowClick={(p) => navigate(paths.landingPage(p.id))}
          pagination={{
            page: pages.data?.page ?? pager.page,
            perPage: pager.perPage,
            total: pages.data?.total ?? 0,
            totalPages: pages.data?.totalPages ?? 0,
            onPageChange: pager.setPage,
          }}
        />
      </Card>
      {creating ? (
        <PageCreateForm
          onClose={() => setCreating(false)}
          onCreated={(page) => {
            toast.success(t('templates.pages.created'));
            setCreating(false);
            navigate(paths.landingPage(page.id));
          }}
        />
      ) : null}
    </div>
  );
}
