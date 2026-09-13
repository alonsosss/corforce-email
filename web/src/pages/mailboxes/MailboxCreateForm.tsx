import { useState } from 'react';
import {
  directoryMeta,
  mailDirectoryApi,
  type CreateMailboxRequest,
  type DirectoryMeta,
  type Mailbox,
} from '@/api/mailDirectory';
import { useAction } from '@/hooks/useAction';
import { useResource } from '@/hooks/useResource';
import { Checkbox, FormField, Input, PasswordInput } from '@/design/components';
import { normalizeDomainName, normalizeLocalPart } from '@/lib/mailAddress';
import type { QuotaAmount } from '@/lib/quota';
import { hasErrors, rules, validateField, type FieldErrors } from '@/lib/validate';
import { t } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { DirectoryDomainPicker } from '@/pages/shared/DirectoryDomainPicker';
import { QuotaField, readQuota } from '@/pages/shared/QuotaField';
import { ResourceGate } from '@/pages/shared/ResourceGate';
import { AccessCheckboxes, MAILBOX_ACCESS_KEYS, allAccess } from './access';

type Field = 'local_part' | 'domain' | 'password' | 'confirm' | 'display_name' | 'quota';

export interface MailboxCreateFormProps {
  onClose: () => void;
  onCreated: (mailbox: Mailbox) => void;
}

export function MailboxCreateForm(props: MailboxCreateFormProps) {
  const meta = useResource(directoryMeta);
  return (
    <ResourceGate
      resource={meta}
      modal={{ title: t('mailboxes.form.createTitle'), onClose: props.onClose }}
    >
      {(rules) => <CreateForm {...props} meta={rules} />}
    </ResourceGate>
  );
}

function CreateForm({
  onClose,
  onCreated,
  meta,
}: MailboxCreateFormProps & { meta: DirectoryMeta }) {
  const [localPart, setLocalPart] = useState('');
  const [domain, setDomain] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [quota, setQuota] = useState<QuotaAmount>({ amount: '', unit: 'GiB' });
  const [access, setAccess] = useState(allAccess(MAILBOX_ACCESS_KEYS, true));
  const [tlsIn, setTlsIn] = useState(false);
  const [tlsOut, setTlsOut] = useState(false);
  const [errors, setErrors] = useState<FieldErrors<Field>>({});

  const action = useAction(async (input: CreateMailboxRequest) => {
    const { data } = await mailDirectoryApi.createMailbox(input);
    onCreated(data);
  });

  const submit = async () => {
    const local = normalizeLocalPart(localPart);
    const domainName = normalizeDomainName(domain);
    const quotaReading = readQuota(quota, true);
    const next: FieldErrors<Field> = {
      local_part:
        validateField(localPart, rules.required) ??
        (local ? null : t('validation.localPart')) ??
        undefined,
      domain:
        validateField(domain, rules.required) ??
        (domainName ? null : t('validation.domain')) ??
        undefined,
      password:
        validateField(
          password,
          rules.required,
          rules.minLength(meta.mailbox.password_min_length),
          rules.maxLength(meta.mailbox.password_max_length),
        ) ?? undefined,
      confirm: confirm === password ? undefined : t('validation.passwordMismatch'),
      display_name:
        validateField(displayName, rules.maxLength(meta.mailbox.display_name_max_length)) ??
        undefined,
      quota: quotaReading.error ?? undefined,
    };
    setErrors(next);
    if (hasErrors(next) || !local || !domainName) return;
    await action.run({
      local_part: local,
      domain: domainName,
      password,
      display_name: displayName.trim(),
      quota_bytes: quotaReading.bytes,
      ...access,
      tls_enforce_in: tlsIn,
      tls_enforce_out: tlsOut,
    });
  };

  return (
    <FormModal
      id="mailbox-create-form"
      title={t('mailboxes.form.createTitle')}
      submitLabel={t('common.create')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={submit}
      size="lg"
    >
      <div className="cf-form__row">
        <FormField
          label={t('mailboxes.form.localPart')}
          htmlFor="mailbox-local"
          required
          error={errors.local_part}
          hint={t('mailboxes.form.localPartHint')}
        >
          <Input
            id="mailbox-local"
            className="cf-mono"
            value={localPart}
            onChange={(e) => setLocalPart(e.target.value)}
            invalid={Boolean(errors.local_part)}
            autoComplete="off"
            spellCheck={false}
          />
        </FormField>
        <FormField
          label={t('mailboxes.form.domain')}
          htmlFor="mailbox-domain"
          required
          error={errors.domain}
        >
          <DirectoryDomainPicker
            id="mailbox-domain"
            placeholder={t('common.select')}
            value={domain}
            onChange={setDomain}
            invalid={Boolean(errors.domain)}
            optionLabel={(d) =>
              d.active ? d.domain : t('mailboxes.form.domainInactive', { domain: d.domain })
            }
          />
        </FormField>
      </div>
      <FormField
        label={t('mailboxes.form.displayName')}
        htmlFor="mailbox-name"
        error={errors.display_name}
      >
        <Input
          id="mailbox-name"
          value={displayName}
          onChange={(e) => setDisplayName(e.target.value)}
          invalid={Boolean(errors.display_name)}
        />
      </FormField>
      <div className="cf-form__row">
        <FormField
          label={t('mailboxes.form.password')}
          htmlFor="mailbox-password"
          required
          error={errors.password}
          hint={t('mailboxes.form.passwordHint', { n: meta.mailbox.password_min_length })}
        >
          <PasswordInput
            id="mailbox-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            invalid={Boolean(errors.password)}
            autoComplete="new-password"
          />
        </FormField>
        <FormField
          label={t('mailboxes.form.confirmPassword')}
          htmlFor="mailbox-confirm"
          required
          error={errors.confirm}
        >
          <PasswordInput
            id="mailbox-confirm"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            invalid={Boolean(errors.confirm)}
            autoComplete="new-password"
          />
        </FormField>
      </div>
      <QuotaField
        id="mailbox-quota"
        label={t('mailboxes.form.quota')}
        value={quota}
        onChange={setQuota}
        error={errors.quota}
        hint={t('mailboxes.form.quotaCreateHint')}
      />
      <div className="cf-form__section">{t('mailboxes.form.access')}</div>
      <AccessCheckboxes keys={MAILBOX_ACCESS_KEYS} value={access} onChange={setAccess} />
      <div className="cf-form__section">{t('mailboxes.form.tls')}</div>
      <Checkbox
        label={t('mailboxes.form.tlsIn')}
        checked={tlsIn}
        onChange={(e) => setTlsIn(e.target.checked)}
      />
      <Checkbox
        label={t('mailboxes.form.tlsOut')}
        checked={tlsOut}
        onChange={(e) => setTlsOut(e.target.checked)}
      />
    </FormModal>
  );
}
