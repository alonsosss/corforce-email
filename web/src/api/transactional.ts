import { api } from './client';
import { endpoints } from './endpoints';

// DTOs de services/transactional/internal/adapters/http/handler.go (sendingDomainResponse):
// la proyeccion local de los dominios de envio de la empresa.

export interface SendingDomain {
  domain: string;
  status: string;
  purpose: string;
  /** null mientras domain-service no ha dicho si SES acepta ya el dominio. */
  sending_ready: boolean | null;
  /** Si hoy se puede enviar desde el dominio, con la misma regla que aplica el envio. */
  can_send: boolean;
  updated_at: string;
}

export const transactionalApi = {
  sendingDomains: async (): Promise<SendingDomain[]> =>
    (await api.get<SendingDomain[] | null>(endpoints.transactional.sendingDomains)).data ?? [],
};
