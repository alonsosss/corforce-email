import { api } from './client';
import { endpoints } from './endpoints';
import { fetchList, fetchPage } from './paging';
import type { Page, PageQuery } from './types';

// DTOs de services/transactional/internal/adapters/http/handler.go (sendingDomainResponse) y de
// domain.Message y domain.Event, que el handler devuelve tal cual.

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

/** Estados de un mensaje (domain.Status* de transactional), en el orden de su ciclo de vida. */
export const MESSAGE_STATUSES = [
  'accepted',
  'queued',
  'sent',
  'delivered',
  'bounced',
  'complained',
  'rejected',
  'failed',
  'suppressed',
] as const;
export type MessageStatus = (typeof MESSAGE_STATUSES)[number];

export interface MessageRecipient {
  email: string;
  name?: string;
}

/** Un mensaje del listado: sin cuerpos ni variables, que el servicio no devuelve en la lista. */
export interface TransactionalMessage {
  id: string;
  from_email: string;
  from_name: string;
  reply_to?: string;
  to: MessageRecipient[] | null;
  cc: MessageRecipient[] | null;
  bcc: MessageRecipient[] | null;
  subject: string;
  template_id?: string;
  template_version?: number;
  status: MessageStatus;
  /** Motivo del ultimo fallo, cuando lo hubo. */
  error?: string;
  attempts: number;
  class: string;
  campaign_id?: string;
  /** api o smtp (relay). */
  origin: string;
  sent_at?: string;
  created_at: string;
  updated_at: string;
  test: boolean;
}

/** Un evento de SES del mensaje: entrega, rebote, queja, apertura, clic... */
export interface MessageEvent {
  id: string;
  type: string;
  recipient: string;
  /** Lo que SES conto: bounce_type, diagnostic_code, reason, link... segun el tipo. */
  detail: Record<string, unknown> | null;
  occurred_at: string;
}

export interface MessageQuery extends PageQuery {
  status?: MessageStatus;
  to?: string;
  from?: string;
  date_from?: string;
  date_to?: string;
}

export const transactionalApi = {
  sendingDomains: async (): Promise<SendingDomain[]> =>
    (await api.get<SendingDomain[] | null>(endpoints.transactional.sendingDomains)).data ?? [],
  messages: (query: MessageQuery): Promise<Page<TransactionalMessage>> =>
    fetchPage<TransactionalMessage>(endpoints.transactional.messages, { ...query }),
  message: async (id: string): Promise<TransactionalMessage> =>
    (await api.get<TransactionalMessage>(endpoints.transactional.message(id))).data,
  events: (id: string): Promise<MessageEvent[]> =>
    fetchList<MessageEvent>(endpoints.transactional.messageEvents(id)),
};
