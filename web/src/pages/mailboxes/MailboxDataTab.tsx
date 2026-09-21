import { useState, type FormEvent } from 'react';
import {
  directoryMeta,
  mailDirectoryApi,
  type ActiveState,
  type DirectoryMeta,
  type Mailbox,
  type UpdateMailboxRequest,
} from '@/api/mailDirectory';
import { errorMessage } from '@/api/messages';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { useResource } from '@/hooks/useResource';
import {
  Button,
  Card,
  Checkbox,
  DescriptionList,
  FormField,
  Input,
  Select,
  useToast,
} from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { changed, isEmptyPatch } from '@/lib/patch';
import { bytesToQuota, type QuotaAmount } from '@/lib/quota';
import { rules, validateField } from '@/lib/validate';
import { t, tEnum } from '@/i18n';
import { QuotaField, readQuota } from '@/pages/shared/QuotaField';
import { ResourceGate } from '@/pages/shared/ResourceGate';
import { AccessCheckboxes, MAILBOX_ACCESS_KEYS, pickAccess } from './access';

export interface MailboxDataTabProps {
  mailbox: Mailbox;
  onChange: (mailbox: Mailbox) => void;
}

export function MailboxDataTab(props: MailboxDataTabProps) {
  const meta = useResource(directoryMeta);
  return (
    <ResourceGate resource={meta}>{(rules) => <DataForm {...props} meta={rules} />}</ResourceGate>
  );
}

function DataForm({ mailbox, onChange, meta }: MailboxDataTabProps & { meta: DirectoryMeta }) {
  const toast = useToast();
  const { can } = useAccess();
  const editable = can(...PERMISSIONS.mailboxes.update);
  const [displayName, setDisplayName] = useState(mailbox.display_name);
  const [active, setActive] = useState<ActiveState>(mailbox.active);
  const [quota, setQuota] = useState<QuotaAmount>(bytesToQuota(mailbox.quota_bytes));
  const [access, setAccess] = useState(pickAccess(MAILBOX_ACCESS_KEYS, mailbox));
  const [tlsIn, setTlsIn] = useState(mailbox.tls_enforce_in);
  const [tlsOut, setTlsOut] = useState(mailbox.tls_enforce_out);
  const [forcePassword, setForcePassword] = useState(mailbox.force_pw_update);
  const [errors, setErrors] = useState<{ display_name?: string; quota?: string }>({});

  const save = useAction(async (body: UpdateMailboxRequest) => {
    const { data } = await mailDirectoryApi.updateMailbox(mailbox.id, body);
    onChange(data);
    toast.success(t('mailboxes.updated'));
  });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const quotaReading = readQuota(quota);
    const next = {
      display_name:
        validateField(displayName, rules.maxLength(meta.mailbox.display_name_max_length)) ??
        undefined,
      quota: quotaReading.error ?? undefined,
    };
    setErrors(next);
    if (next.display_name || next.quota) return;
    const body: UpdateMailboxRequest = {
      display_name: changed(displayName.trim(), mailbox.display_name),
      active: changed(active, mailbox.active),
      quota_bytes: changed(quotaReading.bytes, mailbox.quota_bytes),
      imap_access: changed(access.imap_access, mailbox.imap_access),
      pop3_access: changed(access.pop3_access, mailbox.pop3_access),
      smtp_access: changed(access.smtp_access, mailbox.smtp_access),
      sieve_access: changed(access.sieve_access, mailbox.sieve_access),
      dav_access: changed(access.dav_access, mailbox.dav_access),
      tls_enforce_in: changed(tlsIn, mailbox.tls_enforce_in),
      tls_enforce_out: changed(tlsOut, mailbox.tls_enforce_out),
      force_pw_update: changed(forcePassword, mailbox.force_pw_update),
    };
    if (isEmptyPatch(body)) return;
    await save.run(body);
  };

  return (
    <div className="cf-stack">
      <Card>
        <DescriptionList
          items={[
            {
              label: t('mailboxes.column.address'),
              value: <span className="cf-mono">{mailbox.username}</span>,
            },
            { label: t('common.createdAt'), value: formatDateTime(mailbox.created_at) },
            { label: t('common.updatedAt'), value: formatDateTime(mailbox.updated_at) },
            { label: t('common.id'), value: <span className="cf-mono">{mailbox.id}</span> },
          ]}
        />
      </Card>
      <Card title={t('mailboxes.data.title')}>
        <form className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
          <div className="cf-form__row">
            <FormField
              label={t('mailboxes.form.displayName')}
              htmlFor="mailbox-edit-name"
              error={errors.display_name}
            >
              <Input
                id="mailbox-edit-name"
                value={displayName}
                onChange={(e) => setDisplayName(e.target.value)}
                disabled={!editable}
                invalid={Boolean(errors.display_name)}
              />
            </FormField>
            <FormField
              label={t('common.status')}
              htmlFor="mailbox-edit-status"
              hint={tEnum('mail.activeHint', String(active))}
            >
              <Select
                id="mailbox-edit-status"
                options={meta.mailbox.active_states.map((s) => ({
                  value: String(s.value),
                  label: tEnum('mail.active', String(s.value)),
                }))}
                value={String(active)}
                onChange={(e) => setActive(Number(e.target.value) as ActiveState)}
                disabled={!editable}
              />
            </FormField>
          </div>
          <QuotaField
            id="mailbox-edit-quota"
            label={t('mailboxes.form.quota')}
            value={quota}
            onChange={setQuota}
            error={errors.quota}
            hint={t('mailboxes.form.quotaEditHint')}
            disabled={!editable}
            required
          />
          <div className="cf-form__section">{t('mailboxes.form.access')}</div>
          <AccessCheckboxes
            keys={MAILBOX_ACCESS_KEYS}
            value={access}
            onChange={setAccess}
            disabled={!editable}
          />
          <div className="cf-form__section">{t('mailboxes.form.tls')}</div>
          <Checkbox
            label={t('mailboxes.form.tlsIn')}
            checked={tlsIn}
            onChange={(e) => setTlsIn(e.target.checked)}
            disabled={!editable}
          />
          <Checkbox
            label={t('mailboxes.form.tlsOut')}
            checked={tlsOut}
            onChange={(e) => setTlsOut(e.target.checked)}
            disabled={!editable}
          />
          <div className="cf-form__section">{t('mailboxes.form.security')}</div>
          <Checkbox
            label={t('mailboxes.form.forcePasswordUpdate')}
            checked={forcePassword}
            onChange={(e) => setForcePassword(e.target.checked)}
            disabled={!editable}
          />
          {save.error ? (
            <div className="cf-form__error" role="alert">
              {errorMessage(save.error)}
            </div>
          ) : null}
          {editable ? (
            <div className="cf-form__actions">
              <Button type="submit" variant="primary" loading={save.busy}>
                {t('common.save')}
              </Button>
            </div>
          ) : null}
        </form>
      </Card>
    </div>
  );
}
