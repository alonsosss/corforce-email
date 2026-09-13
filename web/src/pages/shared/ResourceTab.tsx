import { useState, type ComponentType, type ReactNode } from 'react';
import type { CrudPermissions } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import type { Page, PageQuery } from '@/api/types';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import {
  Button,
  Card,
  ConfirmDialog,
  DataTable,
  useToast,
  type Column,
  type PaginationState,
} from '@/design/components';
import { IconPlus } from '@/design/icons';
import { t, type MessageKey } from '@/i18n';
import { RowActions } from './RowActions';

export interface ResourceFormProps<T> {
  /** null en el alta. */
  item: T | null;
  onClose: () => void;
  onSaved: () => void;
}

export interface ResourceTabTexts<T> {
  title: string;
  description?: string;
  create: string;
  empty: string;
  created: string;
  updated: string;
  deleted: string;
  deleteTitle: string;
  deleteConfirm: (row: T) => string;
}

interface CrudCardProps<T> {
  rows: T[];
  rowKey: (row: T) => string;
  loading: boolean;
  error: unknown;
  onRetry: () => void;
  onChanged: () => void;
  pagination?: PaginationState;
  columns: Column<T>[];
  texts: ResourceTabTexts<T>;
  Form: ComponentType<ResourceFormProps<T>>;
  canCreate: boolean;
  canUpdate: boolean;
  canDelete: boolean;
  remove: (row: T) => Promise<unknown>;
  isLocked?: (row: T) => boolean;
  toolbar?: ReactNode;
  deleteErrorOverrides?: Partial<Record<string, MessageKey>>;
}

/**
 * Tabla con alta, edicion y baja de un recurso. Cada boton aparece solo con su permiso y
 * el backend vuelve a comprobarlo.
 */
function CrudCard<T>({
  rows,
  rowKey,
  loading,
  error,
  onRetry,
  onChanged,
  pagination,
  columns,
  texts,
  Form,
  canCreate,
  canUpdate,
  canDelete,
  remove,
  isLocked,
  toolbar,
  deleteErrorOverrides,
}: CrudCardProps<T>) {
  const toast = useToast();
  // undefined: formulario cerrado; null: alta; una fila: edicion.
  const [editing, setEditing] = useState<T | null | undefined>(undefined);
  const [removing, setRemoving] = useState<T | null>(null);

  const allColumns: Column<T>[] = [
    ...columns,
    {
      key: 'actions',
      header: '',
      align: 'right',
      render: (row) => {
        const locked = isLocked?.(row) ?? false;
        return (
          <RowActions
            onEdit={canUpdate && !locked ? () => setEditing(row) : undefined}
            onDelete={canDelete && !locked ? () => setRemoving(row) : undefined}
          />
        );
      },
    },
  ];

  return (
    <Card
      flush
      title={texts.title}
      description={texts.description}
      actions={
        canCreate ? (
          <Button variant="primary" icon={<IconPlus size={16} />} onClick={() => setEditing(null)}>
            {texts.create}
          </Button>
        ) : null
      }
    >
      {toolbar}
      <DataTable
        columns={allColumns}
        rows={rows}
        rowKey={rowKey}
        loading={loading}
        error={error}
        onRetry={onRetry}
        empty={{ title: texts.empty }}
        pagination={pagination}
      />
      {editing !== undefined ? (
        <Form
          item={editing}
          onClose={() => setEditing(undefined)}
          onSaved={() => {
            toast.success(editing ? texts.updated : texts.created);
            setEditing(undefined);
            onChanged();
          }}
        />
      ) : null}
      <ConfirmDialog
        open={removing !== null}
        title={texts.deleteTitle}
        message={removing ? texts.deleteConfirm(removing) : ''}
        confirmLabel={t('common.delete')}
        danger
        errorOverrides={deleteErrorOverrides}
        onCancel={() => setRemoving(null)}
        onConfirm={async () => {
          if (!removing) return;
          await remove(removing);
          toast.success(texts.deleted);
          setRemoving(null);
          onChanged();
        }}
      />
    </Card>
  );
}

export interface ResourceTabProps<T extends { id: string }> {
  permissions: CrudPermissions;
  load: (query: PageQuery) => Promise<Page<T>>;
  remove: (id: string) => Promise<unknown>;
  columns: Column<T>[];
  texts: ResourceTabTexts<T>;
  Form: ComponentType<ResourceFormProps<T>>;
  /** Filas que no se tocan aunque haya permiso (por ejemplo, rutas de plataforma). */
  isLocked?: (row: T) => boolean;
  deleteErrorOverrides?: Partial<Record<string, MessageKey>>;
}

/** Recurso con listado paginado por el meta del envelope. */
export function ResourceTab<T extends { id: string }>({
  permissions,
  load,
  remove,
  ...rest
}: ResourceTabProps<T>) {
  const { can } = useAccess();
  const pager = usePagination();
  const list = useQuery(
    () => load({ page: pager.page, per_page: pager.perPage }),
    [load, pager.page, pager.perPage],
  );

  return (
    <CrudCard
      {...rest}
      rows={list.data?.items ?? []}
      rowKey={(row) => row.id}
      loading={list.loading}
      error={list.error}
      onRetry={list.reload}
      onChanged={list.reload}
      pagination={{
        page: list.data?.page ?? pager.page,
        perPage: pager.perPage,
        total: list.data?.total ?? 0,
        totalPages: list.data?.totalPages ?? 0,
        onPageChange: pager.setPage,
      }}
      canCreate={can(...permissions.create)}
      canUpdate={can(...permissions.update)}
      canDelete={can(...permissions.delete)}
      remove={(row) => remove(row.id)}
    />
  );
}

export interface ListTabProps<T> {
  /** Cambiar la funcion (por ejemplo, al aplicar un filtro) vuelve a pedir la lista. */
  load: () => Promise<T[]>;
  rowKey: (row: T) => string;
  columns: Column<T>[];
  texts: ResourceTabTexts<T>;
  Form: ComponentType<ResourceFormProps<T>>;
  canCreate: boolean;
  canUpdate: boolean;
  canDelete: boolean;
  remove: (row: T) => Promise<unknown>;
  toolbar?: ReactNode;
}

/** Recurso cuyo API devuelve la lista completa, sin paginar. */
export function ListTab<T>({ load, ...rest }: ListTabProps<T>) {
  const list = useQuery(() => load(), [load]);
  return (
    <CrudCard
      {...rest}
      rows={list.data ?? []}
      loading={list.loading}
      error={list.error}
      onRetry={list.reload}
      onChanged={list.reload}
    />
  );
}
