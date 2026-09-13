import type { ReactNode } from 'react';
import { Button } from './Button';
import { EmptyState, ErrorState, Skeleton } from './States';
import { IconChevronLeft, IconChevronRight } from '../icons';
import { t } from '@/i18n';

export interface Column<T> {
  key: string;
  header: ReactNode;
  render: (row: T) => ReactNode;
  align?: 'left' | 'right';
  width?: string;
}

export interface PaginationState {
  page: number;
  perPage: number;
  total: number;
  totalPages: number;
  onPageChange: (page: number) => void;
}

export interface DataTableProps<T> {
  columns: Column<T>[];
  rows: T[];
  rowKey: (row: T) => string;
  loading?: boolean;
  error?: unknown;
  onRetry?: () => void;
  empty?: { title: string; description?: ReactNode; action?: ReactNode };
  pagination?: PaginationState;
  onRowClick?: (row: T) => void;
  skeletonRows?: number;
}

export function DataTable<T>({
  columns,
  rows,
  rowKey,
  loading = false,
  error,
  onRetry,
  empty,
  pagination,
  onRowClick,
  skeletonRows = 5,
}: DataTableProps<T>) {
  const showSkeleton = loading && rows.length === 0;
  const showEmpty = !loading && !error && rows.length === 0;

  return (
    <div>
      <div className="cf-table-wrap">
        <table className="cf-table">
          <thead>
            <tr>
              {columns.map((col) => (
                <th
                  key={col.key}
                  className={col.align === 'right' ? 'cf-table__cell--right' : undefined}
                  style={col.width ? { width: col.width } : undefined}
                  scope="col"
                >
                  {col.header}
                </th>
              ))}
            </tr>
          </thead>
          <tbody aria-busy={loading || undefined}>
            {showSkeleton
              ? Array.from({ length: skeletonRows }, (_, i) => (
                  <tr key={`skeleton-${i}`}>
                    {columns.map((col) => (
                      <td key={col.key}>
                        <Skeleton />
                      </td>
                    ))}
                  </tr>
                ))
              : rows.map((row) => (
                  <tr
                    key={rowKey(row)}
                    className={onRowClick ? 'cf-table__row--clickable' : undefined}
                    onClick={onRowClick ? () => onRowClick(row) : undefined}
                  >
                    {columns.map((col) => (
                      <td
                        key={col.key}
                        className={col.align === 'right' ? 'cf-table__cell--right' : undefined}
                      >
                        {col.render(row)}
                      </td>
                    ))}
                  </tr>
                ))}
          </tbody>
        </table>
      </div>
      {error ? (
        <div className="cf-table__state">
          <ErrorState error={error} onRetry={onRetry} />
        </div>
      ) : null}
      {showEmpty ? (
        <div className="cf-table__state">
          <EmptyState
            title={empty?.title ?? t('common.none')}
            description={empty?.description}
            action={empty?.action}
          />
        </div>
      ) : null}
      {pagination && (rows.length > 0 || pagination.page > 1) ? (
        <Pagination {...pagination} />
      ) : null}
    </div>
  );
}

export function Pagination({ page, total, totalPages, onPageChange }: PaginationState) {
  const lastPage = Math.max(totalPages, 1);
  return (
    <nav className="cf-pagination" aria-label={t('common.pageOf', { page, total: lastPage })}>
      <span>{t('common.totalRows', { total })}</span>
      <div className="cf-pagination__controls">
        <Button
          size="sm"
          iconOnly
          icon={<IconChevronLeft size={16} />}
          disabled={page <= 1}
          onClick={() => onPageChange(page - 1)}
        >
          {t('common.previous')}
        </Button>
        <span>{t('common.pageOf', { page, total: lastPage })}</span>
        <Button
          size="sm"
          iconOnly
          icon={<IconChevronRight size={16} />}
          disabled={page >= lastPage}
          onClick={() => onPageChange(page + 1)}
        >
          {t('common.next')}
        </Button>
      </div>
    </nav>
  );
}
