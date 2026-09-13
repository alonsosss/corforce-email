import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  TEMPLATE_KINDS,
  TEMPLATE_STATUSES,
  templatesApi,
  type Template,
  type TemplateKind,
  type TemplateStatus,
} from '@/api/templates';
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
import { TemplateCreateForm } from './TemplateCreateForm';
import { templateKindTone, templateStatusTone } from './templateStatus';

const SEARCH_DEBOUNCE_MS = 300;

export default function TemplatesPage() {
  const navigate = useNavigate();
  const toast = useToast();
  const { can } = useAccess();
  const pager = usePagination();
  const [kind, setKind] = useState<TemplateKind | ''>('');
  const [status, setStatus] = useState<TemplateStatus | ''>('');
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

  const templates = useQuery(
    () =>
      templatesApi.list({
        page: pager.page,
        per_page: pager.perPage,
        kind: kind || undefined,
        status: status || undefined,
        search: search || undefined,
      }),
    [pager.page, pager.perPage, kind, status, search],
  );

  const columns: Column<Template>[] = [
    {
      key: 'name',
      header: t('common.name'),
      render: (tpl) => (
        <div className="cf-cell-stack">
          <strong>{tpl.name}</strong>
          {tpl.description ? (
            <span className="cf-text-muted cf-text-sm">{tpl.description}</span>
          ) : null}
        </div>
      ),
    },
    {
      key: 'kind',
      header: t('templates.column.kind'),
      render: (tpl) => (
        <Badge tone={templateKindTone(tpl.kind)}>{tEnum('templates.kind', tpl.kind)}</Badge>
      ),
    },
    {
      key: 'status',
      header: t('common.status'),
      render: (tpl) => (
        <Badge tone={templateStatusTone(tpl.status)}>{tEnum('templates.status', tpl.status)}</Badge>
      ),
    },
    {
      key: 'version',
      header: t('templates.column.published'),
      render: (tpl) =>
        tpl.current_version > 0 ? (
          t('templates.versionLabel', { n: tpl.current_version })
        ) : (
          <Badge tone="warning">{t('templates.unpublished')}</Badge>
        ),
    },
    {
      key: 'updated',
      header: t('common.updatedAt'),
      render: (tpl) => formatDateTime(tpl.updated_at),
    },
  ];

  return (
    <div>
      <PageHeader
        title={t('templates.title')}
        description={t('templates.subtitle')}
        actions={
          can(...PERMISSIONS.templates.create) ? (
            <Button
              variant="primary"
              icon={<IconPlus size={16} />}
              onClick={() => setCreating(true)}
            >
              {t('templates.new')}
            </Button>
          ) : null
        }
      />
      <Card flush>
        <div className="cf-toolbar">
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="templates-search">
              {t('common.search')}
            </label>
            <Input
              id="templates-search"
              placeholder={t('templates.searchPlaceholder')}
              value={searchInput}
              onChange={(e) => setSearchInput(e.target.value)}
            />
          </div>
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="templates-kind">
              {t('templates.column.kind')}
            </label>
            <Select
              id="templates-kind"
              placeholder={t('common.all')}
              options={TEMPLATE_KINDS.map((k) => ({ value: k, label: tEnum('templates.kind', k) }))}
              value={kind}
              onChange={(e) => {
                setKind(e.target.value as TemplateKind | '');
                pager.reset();
              }}
            />
          </div>
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="templates-status">
              {t('common.status')}
            </label>
            <Select
              id="templates-status"
              placeholder={t('common.all')}
              options={TEMPLATE_STATUSES.map((s) => ({
                value: s,
                label: tEnum('templates.status', s),
              }))}
              value={status}
              onChange={(e) => {
                setStatus(e.target.value as TemplateStatus | '');
                pager.reset();
              }}
            />
          </div>
        </div>
        <DataTable
          columns={columns}
          rows={templates.data?.items ?? []}
          rowKey={(tpl) => tpl.id}
          loading={templates.loading}
          error={templates.error}
          onRetry={templates.reload}
          empty={{ title: t('templates.empty'), description: t('templates.emptyDescription') }}
          onRowClick={(tpl) => navigate(paths.template(tpl.id))}
          pagination={{
            page: templates.data?.page ?? pager.page,
            perPage: pager.perPage,
            total: templates.data?.total ?? 0,
            totalPages: templates.data?.totalPages ?? 0,
            onPageChange: pager.setPage,
          }}
        />
      </Card>
      {creating ? (
        <TemplateCreateForm
          onClose={() => setCreating(false)}
          onCreated={(template) => {
            toast.success(t('templates.created'));
            setCreating(false);
            navigate(paths.template(template.id));
          }}
        />
      ) : null}
    </div>
  );
}
