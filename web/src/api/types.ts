// Envelope de pkg/response/response.go: {data, error:{code,message}, meta:{...}}.
// Los campos de meta llevan omitempty en Go: un total de 0 llega ausente, no como 0.

export interface ApiErrorBody {
  code: string;
  message: string;
  /** Datos para explicar el error (el campo que fallo, la direccion rechazada). */
  details?: Record<string, string>;
}

export interface Meta {
  page?: number;
  per_page?: number;
  total?: number;
  total_pages?: number;
}

export interface Envelope<T> {
  data?: T;
  error?: ApiErrorBody;
  meta?: Meta;
}

export interface ApiResponse<T> {
  data: T;
  meta?: Meta;
}

export interface Page<T> {
  items: T[];
  page: number;
  perPage: number;
  total: number;
  totalPages: number;
}

export interface PageQuery {
  page?: number;
  per_page?: number;
}

export function toPage<T>(res: ApiResponse<T[]>, fallback: Required<PageQuery>): Page<T> {
  const meta = res.meta ?? {};
  return {
    items: res.data ?? [],
    page: meta.page ?? fallback.page,
    perPage: meta.per_page ?? fallback.per_page,
    total: meta.total ?? 0,
    totalPages: meta.total_pages ?? 0,
  };
}
