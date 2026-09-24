import { api } from './client';
import { endpoints } from './endpoints';
import { webmailRequest } from './webmail';

/*
 * Asistente del webmail (docs/adr/0015-asistente-del-webmail-con-claude.md). Lo que devuelve es
 * texto propuesto que el usuario revisa: nada se envia ni se guarda. El webmail lee el mensaje del
 * buzon por carpeta y UID; aqui solo viaja el borrador que escribe el propio usuario.
 *
 * DTOs de services/webmail/internal/adapters/http/assistant_handlers.go y, el ajuste por empresa, de
 * services/mail-directory/internal/adapters/http/assistant.go.
 */

export type AssistantAction = 'summarize' | 'reply' | 'tone' | 'extract';
export type AssistantTone = 'formal' | 'friendly' | 'brief';

/** GET /webmail/assistant: si se ofrece, los topes servidos y lo usado hoy. */
export interface AssistantStatus {
  available: boolean;
  /** not_configured: la plataforma no tiene clave; disabled: la empresa no lo activo. */
  reason?: 'not_configured' | 'disabled';
  actions: AssistantAction[];
  tones: AssistantTone[];
  limits: {
    max_input_chars: number;
    max_thread_messages: number;
    max_instruction_chars: number;
    mailbox_daily: number;
    tenant_daily: number;
  };
  usage: { mailbox: number; tenant: number };
}

export interface AssistantMessageRef {
  folder: string;
  uid: number;
}

export interface AssistantText {
  text: string;
  /** Se recorto el texto enviado para respetar el tope. */
  input_truncated: boolean;
  /** El proveedor corto la salida por su tope. */
  output_truncated: boolean;
}

export interface ExtractedTask {
  title: string;
  /** AAAA-MM-DD o vacio. */
  due_date: string;
}

/** Cita propuesta en la hora local que dice el correo. start_time vacio es de todo el dia. */
export interface ExtractedEvent {
  title: string;
  date: string;
  start_time: string;
  end_time: string;
  location: string;
  notes: string;
}

export interface AssistantExtraction {
  tasks: ExtractedTask[];
  events: ExtractedEvent[];
  input_truncated: boolean;
}

/** GET/PUT /mail-directory/assistant: el interruptor de la empresa en el panel. */
export interface AssistantSettings {
  enabled: boolean;
  enabled_at: string | null;
  updated_by: string | null;
  updated_at: string | null;
}

const wm = endpoints.webmail;

export const assistantApi = {
  status: (signal?: AbortSignal) =>
    webmailRequest<AssistantStatus>('GET', wm.assistant, { signal }),
  summarize: (messages: AssistantMessageRef[]) =>
    webmailRequest<AssistantText>('POST', wm.assistantSummarize, { json: { messages } }),
  reply: (ref: AssistantMessageRef, instructions: string) =>
    webmailRequest<AssistantText>('POST', wm.assistantReply, { json: { ...ref, instructions } }),
  tone: (text: string, tone: AssistantTone) =>
    webmailRequest<AssistantText>('POST', wm.assistantTone, { json: { text, tone } }),
  extract: (ref: AssistantMessageRef, today: string) =>
    webmailRequest<AssistantExtraction>('POST', wm.assistantExtract, { json: { ...ref, today } }),
};

export const assistantSettingsApi = {
  get: () => api.get<AssistantSettings>(endpoints.mailDirectory.assistant),
  set: (enabled: boolean) =>
    api.put<AssistantSettings>(endpoints.mailDirectory.assistant, { body: { enabled } }),
};
