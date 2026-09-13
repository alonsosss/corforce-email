import type { ReactNode } from 'react';
import type { QueryState } from '@/hooks/useQuery';
import { Card, ErrorState, Modal, Skeleton } from '@/design/components';

/**
 * Pinta los hijos solo cuando el recurso (un catalogo del API) esta cargado; mientras,
 * el esqueleto o el error con reintento. En un modal si el contenido lo es.
 */
export function ResourceGate<T>({
  resource,
  children,
  modal,
}: {
  resource: QueryState<T>;
  children: (data: T) => ReactNode;
  modal?: { title: string; onClose: () => void };
}) {
  if (resource.data !== null) return <>{children(resource.data)}</>;
  const body = resource.error ? (
    <ErrorState error={resource.error} onRetry={resource.reload} />
  ) : (
    <Skeleton lines={5} />
  );
  if (modal) {
    return (
      <Modal open title={modal.title} onClose={modal.onClose}>
        {body}
      </Modal>
    );
  }
  return <Card>{body}</Card>;
}
