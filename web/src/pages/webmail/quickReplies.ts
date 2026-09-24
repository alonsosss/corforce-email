import type { MailAddress, MailMessage } from '@/api/webmail';
import { getLocale, type MessageKey } from '@/i18n';
import { escapeHtml } from './richText';

/**
 * Variables de una respuesta rapida. Se guardan tal cual en el servicio y se resuelven aqui al
 * insertarlas, con los datos del destinatario y del buzon. Una variable sin dato se deja escrita
 * para que quien redacta la vea y la complete.
 */
export const QUICK_REPLY_VARIABLES = [
  'nombre',
  'empresa',
  'correo',
  'fecha',
  'mi_nombre',
  'mi_correo',
] as const;

export type QuickReplyVariable = (typeof QUICK_REPLY_VARIABLES)[number];

export const QUICK_REPLY_VARIABLE_LABELS: Record<QuickReplyVariable, MessageKey> = {
  nombre: 'webmail.quickReplies.var.nombre',
  empresa: 'webmail.quickReplies.var.empresa',
  correo: 'webmail.quickReplies.var.correo',
  fecha: 'webmail.quickReplies.var.fecha',
  mi_nombre: 'webmail.quickReplies.var.miNombre',
  mi_correo: 'webmail.quickReplies.var.miCorreo',
};

export interface QuickReplyContext {
  /** Primer destinatario del mensaje que se redacta. */
  recipient?: string;
  /** Nombres visibles conocidos por direccion (los del mensaje al que se responde). */
  names?: Readonly<Record<string, string>>;
  mailbox: { email: string; name: string };
  now: Date;
}

function titleCase(words: string): string {
  return words
    .split(/\s+/)
    .filter(Boolean)
    .map((word) => word.charAt(0).toLocaleUpperCase(getLocale()) + word.slice(1))
    .join(' ');
}

/** Nombre a partir de la parte local: "luis.perez" es "Luis Perez". */
function nameFromAddress(email: string): string {
  const local = email.slice(0, email.lastIndexOf('@'));
  return titleCase(local.replace(/[._+-]+/g, ' '));
}

/**
 * Empresa a partir del dominio: la etiqueta anterior al sufijo publico, que se aproxima quitando la
 * ultima etiqueta y, con tres o mas, una segunda de hasta tres letras (com.pe, co.uk).
 */
export function companyFromAddress(email: string): string {
  const labels = email
    .slice(email.lastIndexOf('@') + 1)
    .toLowerCase()
    .split('.')
    .filter(Boolean);
  if (labels.length < 2) return '';
  labels.pop();
  if (labels.length >= 2 && (labels[labels.length - 1]?.length ?? 0) <= 3) labels.pop();
  return titleCase(labels[labels.length - 1] ?? '');
}

/** Valores de las variables que se pueden resolver con el contexto. */
export function quickReplyValues(
  ctx: QuickReplyContext,
): Partial<Record<QuickReplyVariable, string>> {
  const values: Partial<Record<QuickReplyVariable, string>> = {
    fecha: new Intl.DateTimeFormat(getLocale(), { dateStyle: 'long' }).format(ctx.now),
    mi_correo: ctx.mailbox.email,
    mi_nombre: ctx.mailbox.name.trim() || nameFromAddress(ctx.mailbox.email),
  };
  const recipient = ctx.recipient?.trim().toLowerCase();
  if (recipient && recipient.includes('@')) {
    values.correo = recipient;
    values.nombre = ctx.names?.[recipient]?.trim() || nameFromAddress(recipient);
    const company = companyFromAddress(recipient);
    if (company) values.empresa = company;
  }
  return values;
}

/** Sustituye las variables conocidas; en HTML el valor entra escapado. */
export function resolveQuickReply(
  template: string,
  values: Partial<Record<QuickReplyVariable, string>>,
  html: boolean,
): string {
  return template.replace(/\{(\w+)\}/g, (match, name: string) => {
    const value = values[name as QuickReplyVariable];
    if (value === undefined) return match;
    return html ? escapeHtml(value) : value;
  });
}

/** Nombres visibles de las direcciones de un mensaje, para {nombre} al responderlo. */
export function namesOf(
  message: Pick<MailMessage, 'from' | 'to' | 'cc' | 'reply_to'>,
): Record<string, string> {
  const out: Record<string, string> = {};
  const add = (list: readonly MailAddress[]) => {
    for (const address of list) {
      const email = address.email.toLowerCase();
      if (address.name.trim() && !out[email]) out[email] = address.name.trim();
    }
  };
  add(message.reply_to);
  add(message.from);
  add(message.to);
  add(message.cc);
  return out;
}
