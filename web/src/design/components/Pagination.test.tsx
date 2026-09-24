import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { t } from '@/i18n';
import { toPage } from '@/api/types';
import { Pagination } from './DataTable';

describe('total del paginador', () => {
  it('da el numero exacto cuando el servicio cuenta todo', () => {
    render(<Pagination page={1} perPage={20} total={137} totalPages={7} onPageChange={vi.fn()} />);
    expect(screen.getByText(t('common.totalRows', { total: 137 }))).toBeInTheDocument();
  });

  it('dice "más de" cuando el total es un tope', () => {
    render(
      <Pagination
        page={2}
        perPage={20}
        total={10000}
        totalPages={500}
        totalCapped
        onPageChange={vi.fn()}
      />,
    );
    expect(screen.getByText('Más de 10000 registros')).toBeInTheDocument();
    expect(screen.queryByText(t('common.totalRows', { total: 10000 }))).toBeNull();
  });
});

describe('meta del listado paginado', () => {
  const fallback = { page: 1, per_page: 20 };

  it('propaga total_capped', () => {
    const page = toPage(
      { data: [], meta: { page: 3, per_page: 50, total: 10000, total_pages: 200, total_capped: true } },
      fallback,
    );
    expect(page).toMatchObject({ total: 10000, totalPages: 200, totalCapped: true });
  });

  it('sin total_capped el total es exacto', () => {
    const page = toPage({ data: [], meta: { page: 1, per_page: 20, total: 5, total_pages: 1 } }, fallback);
    expect(page.totalCapped).toBe(false);
  });
});
