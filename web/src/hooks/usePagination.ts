import { useCallback, useState } from 'react';

export const DEFAULT_PER_PAGE = 20;

export interface PaginationControls {
  page: number;
  perPage: number;
  setPage: (page: number) => void;
  reset: () => void;
}

export function usePagination(perPage = DEFAULT_PER_PAGE): PaginationControls {
  const [page, setPage] = useState(1);
  const reset = useCallback(() => setPage(1), []);
  return { page, perPage, setPage, reset };
}
