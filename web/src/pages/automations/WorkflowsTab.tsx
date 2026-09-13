import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  automationsApi,
  automationsMeta,
  type Workflow,
  type WorkflowStatus,
} from '@/api/automations';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import { useResource } from '@/hooks/useResource';
import { Card, DataTable, Input, Select, type Column } from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { getLocale, t, tEnum } from '@/i18n';
import { paths } from '@/paths';
import { WorkflowStatusBadge } from './automationStatus';

const SEARCH_DEBOUNCE_MS = 300;

export function WorkflowsTab() {
  const navigate = useNavigate();
  const meta = useResource(automationsMeta);
  const pager = usePagination();
  const [status, setStatus] = useState<WorkflowStatus | ''>('');
  const [searchInput, setSearchInput] = useState('');
  const [search, setSearch] = useState('');
  const format = new Intl.NumberFormat(getLocale());

  useEffect(() => {
    const handle = window.setTimeout(() => {
      setSearch(searchInput.trim());
      pager.reset();
    }, SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(handle);
    // pager.reset es estable; solo el texto escrito dispara la busqueda.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [searchInput]);

  const workflows = useQuery(
    () =>
      automationsApi.listWorkflows({
        page: pager.page,
        per_page: pager.perPage,
        status: status || undefined,
        search: search || undefined,
      }),
    [pager.page, pager.perPage, status, search],
  );

  const columns: Column<Workflow>[] = [
    {
      key: 'name',
      header: t('common.name'),
      render: (w) => (
        <div className="cf-cell-stack">
          <strong>{w.name}</strong>
          {w.description ? <span className="cf-text-muted cf-text-sm">{w.description}</span> : null}
        </div>
      ),
    },
    {
      key: 'status',
      header: t('common.status'),
      render: (w) => <WorkflowStatusBadge status={w.status} />,
    },
    {
      key: 'trigger',
      header: t('automations.editor.trigger'),
      render: (w) => tEnum('automations.trigger', w.trigger.type),
    },
    {
      key: 'steps',
      header: t('automations.column.steps'),
      align: 'right',
      render: (w) => format.format(w.steps?.length ?? 0),
    },
    {
      key: 'activated',
      header: t('automations.column.activatedAt'),
      render: (w) => (w.activated_at ? formatDateTime(w.activated_at) : t('common.dash')),
    },
    { key: 'updated', header: t('common.updatedAt'), render: (w) => formatDateTime(w.updated_at) },
  ];

  return (
    <Card flush>
      <div className="cf-toolbar">
        <div className="cf-field">
          <label className="cf-field__label" htmlFor="workflows-search">
            {t('common.search')}
          </label>
          <Input
            id="workflows-search"
            type="search"
            placeholder={t('automations.searchPlaceholder')}
            value={searchInput}
            maxLength={meta.data?.limits.max_search_length}
            onChange={(e) => setSearchInput(e.target.value)}
          />
        </div>
        <div className="cf-field">
          <label className="cf-field__label" htmlFor="workflows-status">
            {t('common.status')}
          </label>
          <Select
            id="workflows-status"
            placeholder={t('common.all')}
            options={(meta.data?.statuses ?? []).map(({ status: s }) => ({
              value: s,
              label: tEnum('automations.status', s),
            }))}
            value={status}
            onChange={(e) => {
              setStatus(e.target.value as WorkflowStatus | '');
              pager.reset();
            }}
            disabled={!meta.data}
          />
        </div>
      </div>
      <DataTable
        columns={columns}
        rows={workflows.data?.items ?? []}
        rowKey={(w) => w.id}
        loading={workflows.loading}
        error={workflows.error}
        onRetry={workflows.reload}
        empty={{
          title: search || status ? t('automations.noMatches') : t('automations.empty'),
          description: search || status ? undefined : t('automations.emptyDescription'),
        }}
        onRowClick={(w) => navigate(paths.automation(w.id))}
        pagination={{
          page: workflows.data?.page ?? pager.page,
          perPage: pager.perPage,
          total: workflows.data?.total ?? 0,
          totalPages: workflows.data?.totalPages ?? 0,
          onPageChange: pager.setPage,
        }}
      />
    </Card>
  );
}
