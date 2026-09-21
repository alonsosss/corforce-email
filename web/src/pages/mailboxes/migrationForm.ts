import type { CreateMigrationRequest, MigrationMeta, MigrationTls } from '@/api/mailMigration';
import { rules, validateField, type FieldErrors } from '@/lib/validate';
import { t } from '@/i18n';

/** Estado del formulario. La contrasena solo existe aqui y nunca sale de este estado. */
export interface MigrationFormState {
  host: string;
  port: string;
  tls: MigrationTls;
  username: string;
  password: string;
}

export type MigrationFormField = 'host' | 'port' | 'tls' | 'username' | 'password';

/** TLS habitual de un puerto si el servicio lo permite; si no, el actual o el primero que ofrece. */
function tlsForPort(meta: MigrationMeta, port: string, current?: MigrationTls): MigrationTls {
  const preferred = meta.default_tls_for_port[port];
  if (preferred && meta.source_tls_modes.includes(preferred)) return preferred;
  if (current && meta.source_tls_modes.includes(current)) return current;
  return meta.source_tls_modes[0] ?? '';
}

/** Puerto inicial: el primero cuyo TLS habitual es implicito (ssl), y si no el primero que ofrece el servicio. */
export function initialMigrationForm(meta: MigrationMeta): MigrationFormState {
  const port =
    meta.source_ports.find((p) => meta.default_tls_for_port[String(p)] === 'ssl') ??
    meta.source_ports[0];
  const portText = port === undefined ? '' : String(port);
  return {
    host: '',
    port: portText,
    tls: tlsForPort(meta, portText),
    username: '',
    password: '',
  };
}

/** Al cambiar de puerto el TLS pasa al habitual de ese puerto. */
export function changeMigrationPort(
  form: MigrationFormState,
  port: string,
  meta: MigrationMeta,
): MigrationFormState {
  return { ...form, port, tls: tlsForPort(meta, port, form.tls) };
}

export function validateMigrationForm(
  form: MigrationFormState,
  meta: MigrationMeta,
): FieldErrors<MigrationFormField> {
  const required = t('validation.required');
  return {
    host: validateField(form.host, rules.required) ?? undefined,
    port: meta.source_ports.map(String).includes(form.port) ? undefined : required,
    tls: meta.source_tls_modes.includes(form.tls) ? undefined : required,
    username: validateField(form.username, rules.required) ?? undefined,
    password: form.password === '' ? required : undefined,
  };
}

export interface MigrationSubmission {
  request: CreateMigrationRequest;
  /** El formulario tras enviar: lo mismo, sin la contrasena. */
  next: MigrationFormState;
}

/**
 * Arma la peticion y devuelve el formulario ya sin la contrasena: quien envia lo aplica antes de
 * esperar la respuesta, de modo que la contrasena no sobrevive ni al exito ni al fallo.
 */
export function takeMigrationSubmission(
  mailboxId: string,
  form: MigrationFormState,
): MigrationSubmission {
  return {
    request: {
      mailbox_id: mailboxId,
      source_host: form.host.trim(),
      source_port: Number(form.port),
      source_tls: form.tls,
      source_username: form.username.trim(),
      source_password: form.password,
    },
    next: { ...form, password: '' },
  };
}
