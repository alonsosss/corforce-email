import { useNavigate } from 'react-router-dom';
import { segmentsApi, type Segment } from '@/api/segments';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import { Button, Card, DataTable, PageHeader, type Column } from '@/design/components';
import { IconPlus } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { t } from '@/i18n';
import { paths } from '@/paths';

export default function SegmentsPage() {
  const navigate = useNavigate();
  const { can } = useAccess();
  const pager = usePagination();
  const segments = useQuery(
    () => segmentsApi.list({ page: pager.page, per_page: pager.perPage }),
    [pager.page, pager.perPage],
  );

  const columns: Column<Segment>[] = [
    {
      key: 'name',
      header: t('common.name'),
      render: (s) => (
        <div className="cf-cell-stack">
          <strong>{s.name}</strong>
          {s.description ? <span className="cf-text-muted cf-text-sm">{s.description}</span> : null}
        </div>
      ),
    },
    { key: 'updated', header: t('common.updatedAt'), render: (s) => formatDateTime(s.updated_at) },
  ];

  return (
    <div>
      <PageHeader
        title={t('segments.title')}
        description={t('segments.subtitle')}
        actions={
          can(...PERMISSIONS.segments.create) ? (
            <Button
              variant="primary"
              icon={<IconPlus size={16} />}
              onClick={() => navigate(paths.segmentNew)}
            >
              {t('segments.new')}
            </Button>
          ) : null
        }
      />
      <Card flush>
        <DataTable
          columns={columns}
          rows={segments.data?.items ?? []}
          rowKey={(s) => s.id}
          loading={segments.loading}
          error={segments.error}
          onRetry={segments.reload}
          empty={{ title: t('segments.empty'), description: t('segments.emptyDescription') }}
          onRowClick={(s) => navigate(paths.segment(s.id))}
          pagination={{
            page: segments.data?.page ?? pager.page,
            perPage: pager.perPage,
            total: segments.data?.total ?? 0,
            totalPages: segments.data?.totalPages ?? 0,
            onPageChange: pager.setPage,
          }}
        />
      </Card>
    </div>
  );
}
