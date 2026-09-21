import { useRef, useState, type FormEvent } from 'react';
import type { CreateMigrationRequest, MigrationMeta } from '@/api/mailMigration';
import { useAction } from '@/hooks/useAction';
import { Alert, Button, Card, FormField, Input, Select } from '@/design/components';
import { hasErrors, type FieldErrors } from '@/lib/validate';
import { t, tEnum } from '@/i18n';
import { migrationErrorMessage } from './migration';
import {
  changeMigrationPort,
  initialMigrationForm,
  takeMigrationSubmission,
  validateMigrationForm,
  type MigrationFormField,
  type MigrationFormState,
} from './migrationForm';

/** Modo TLS que el servicio permite solo si el operador lo habilito: el trafico y la contrasena van sin cifrar. */
const PLAINTEXT_TLS = 'none';

export interface MigrationFormProps {
  mailboxId: string;
  meta: MigrationMeta;
  onSubmit: (request: CreateMigrationRequest) => Promise<void>;
}

/**
 * Datos del buzon de origen. La contrasena vive solo en este estado: se borra en cuanto se
 * envia, haya exito o no, y no pasa por ninguna cache ni almacenamiento del navegador.
 */
export function MigrationForm({ mailboxId, meta, onSubmit }: MigrationFormProps) {
  const [form, setForm] = useState<MigrationFormState>(() => initialMigrationForm(meta));
  const [errors, setErrors] = useState<FieldErrors<MigrationFormField>>({});
  const passwordRef = useRef<HTMLInputElement>(null);
  const send = useAction(onSubmit);

  const set = <K extends keyof MigrationFormState>(key: K, value: MigrationFormState[K]) =>
    setForm((current) => ({ ...current, [key]: value }));

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const next = validateMigrationForm(form, meta);
    setErrors(next);
    if (hasErrors(next)) return;
    const submission = takeMigrationSubmission(mailboxId, form);
    setForm(submission.next);
    const ok = await send.run(submission.request);
    if (!ok) passwordRef.current?.focus();
  };

  return (
    <Card title={t('migration.form.title')} description={t('migration.form.description')}>
      <form
        className="cf-form"
        onSubmit={(e) => void submit(e)}
        noValidate
        style={{ maxWidth: 560 }}
      >
        <Alert tone="info">{t('migration.form.passwordNotice')}</Alert>
        <Alert tone="info">{t('migration.form.appPasswordNotice')}</Alert>
        <FormField
          label={t('migration.form.host')}
          htmlFor="migration-host"
          required
          error={errors.host}
          hint={t('migration.form.hostHint')}
        >
          <Input
            id="migration-host"
            value={form.host}
            onChange={(e) => set('host', e.target.value)}
            invalid={Boolean(errors.host)}
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
          />
        </FormField>
        <div className="cf-split">
          <FormField
            label={t('migration.form.port')}
            htmlFor="migration-port"
            required
            error={errors.port}
          >
            <Select
              id="migration-port"
              value={form.port}
              onChange={(e) => setForm(changeMigrationPort(form, e.target.value, meta))}
              options={meta.source_ports.map((port) => ({
                value: String(port),
                label: String(port),
              }))}
              invalid={Boolean(errors.port)}
            />
          </FormField>
          <FormField
            label={t('migration.form.tls')}
            htmlFor="migration-tls"
            required
            error={errors.tls}
          >
            <Select
              id="migration-tls"
              value={form.tls}
              onChange={(e) => set('tls', e.target.value)}
              options={meta.source_tls_modes.map((mode) => ({
                value: mode,
                label: tEnum('migration.tls', mode),
              }))}
              invalid={Boolean(errors.tls)}
            />
          </FormField>
        </div>
        {form.tls === PLAINTEXT_TLS ? (
          <Alert tone="warning">{t('migration.form.plaintextWarning')}</Alert>
        ) : null}
        <FormField
          label={t('migration.form.username')}
          htmlFor="migration-username"
          required
          error={errors.username}
          hint={t('migration.form.usernameHint')}
        >
          <Input
            id="migration-username"
            value={form.username}
            onChange={(e) => set('username', e.target.value)}
            invalid={Boolean(errors.username)}
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
          />
        </FormField>
        <FormField
          label={t('migration.form.password')}
          htmlFor="migration-password"
          required
          error={errors.password}
          hint={t('migration.form.passwordHint')}
        >
          <Input
            id="migration-password"
            ref={passwordRef}
            type="password"
            value={form.password}
            onChange={(e) => set('password', e.target.value)}
            invalid={Boolean(errors.password)}
            autoComplete="new-password"
            spellCheck={false}
          />
        </FormField>
        {send.error ? (
          <div className="cf-form__error" role="alert">
            {migrationErrorMessage(send.error)}
          </div>
        ) : null}
        <div className="cf-form__actions">
          <Button type="submit" variant="primary" loading={send.busy}>
            {t('migration.form.submit')}
          </Button>
        </div>
      </form>
    </Card>
  );
}
