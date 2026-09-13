import { useState } from 'react';
import { organizationApi, type Cell, type CellStatus } from '@/api/organization';
import { useQuery } from '@/hooks/useQuery';
import {
  Badge,
  Button,
  Card,
  DataTable,
  PageHeader,
  useToast,
  type BadgeTone,
  type Column,
} from '@/design/components';
import { IconEdit, IconPlus } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { t, tEnum } from '@/i18n';
import { CellForm } from './CellForm';

function cellStatusTone(status: CellStatus): BadgeTone {
  if (status === 'active') return 'success';
  if (status === 'draining') return 'warning';
  return 'neutral';
}

export default function CellsPage() {
  const toast = useToast();
  const cells = useQuery(async () => (await organizationApi.listCells()).data, []);
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<Cell | null>(null);

  const columns: Column<Cell>[] = [
    {
      key: 'code',
      header: t('cells.column.code'),
      render: (c) => <span className="cf-mono">{c.code}</span>,
    },
    { key: 'region', header: t('cells.column.region'), render: (c) => c.region },
    {
      key: 'status',
      header: t('cells.column.status'),
      render: (c) => (
        <Badge tone={cellStatusTone(c.status)}>{tEnum('cells.status', c.status)}</Badge>
      ),
    },
    {
      key: 'host',
      header: t('cells.column.dbHost'),
      render: (c) => <span className="cf-mono">{c.db_host}</span>,
    },
    { key: 'port', header: t('cells.column.dbPort'), align: 'right', render: (c) => c.db_port },
    { key: 'created', header: t('common.createdAt'), render: (c) => formatDateTime(c.created_at) },
    {
      key: 'actions',
      header: '',
      align: 'right',
      render: (c) => (
        <Button
          size="sm"
          variant="ghost"
          icon={<IconEdit size={14} />}
          onClick={() => setEditing(c)}
        >
          {t('common.edit')}
        </Button>
      ),
    },
  ];

  return (
    <div>
      <PageHeader
        title={t('cells.title')}
        description={t('cells.subtitle')}
        actions={
          <Button variant="primary" icon={<IconPlus size={16} />} onClick={() => setCreating(true)}>
            {t('cells.new')}
          </Button>
        }
      />
      <Card flush>
        <DataTable
          columns={columns}
          rows={cells.data ?? []}
          rowKey={(c) => c.id}
          loading={cells.loading}
          error={cells.error}
          onRetry={cells.reload}
          empty={{ title: t('cells.empty') }}
        />
      </Card>
      {creating ? (
        <CellForm
          mode="create"
          open
          onClose={() => setCreating(false)}
          onSubmit={async (input) => {
            await organizationApi.createCell(input);
            toast.success(t('cells.created'));
            setCreating(false);
            cells.reload();
          }}
        />
      ) : null}
      {editing ? (
        <CellForm
          mode="edit"
          open
          cell={editing}
          onClose={() => setEditing(null)}
          onSubmit={async (input) => {
            if (input.region === undefined && input.status === undefined) {
              setEditing(null);
              return;
            }
            await organizationApi.updateCell(editing.id, input);
            toast.success(t('cells.updated'));
            setEditing(null);
            cells.reload();
          }}
        />
      ) : null}
    </div>
  );
}
