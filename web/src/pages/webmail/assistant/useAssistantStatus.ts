import { assistantApi, type AssistantStatus } from '@/api/webmailAssistant';
import { useQuery } from '@/hooks/useQuery';

/**
 * Estado del asistente para la sesion del buzon. Sin clave en la plataforma, sin activar en la empresa
 * o si no se puede leer, la interfaz no lo ofrece: status es null.
 */
export function useAssistantStatus(): { status: AssistantStatus | null; reload: () => void } {
  const query = useQuery((signal) => assistantApi.status(signal), []);
  const status = query.data?.available ? query.data : null;
  return { status, reload: query.reload };
}
