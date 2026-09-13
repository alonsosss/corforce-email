import { useState, type FormEvent } from 'react';
import {
  directoryMeta,
  mailDirectoryApi,
  mailRoutingApi,
  type DirectoryDomain,
  type UpdateDirectoryDomainRequest,
} from '@/api/mailDirectory';
import { errorMessage } from '@/api/messages';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import { Button, Checkbox, FormField, Input, Modal, Select } from '@/design/components';
import { bytesToQuota, type QuotaAmount } from '@/lib/quota';
import { hasErrors, rules, validateField, type FieldErrors } from '@/lib/validate';
import { t } from '@/i18n';
import { QuotaField, readQuota } from '@/pages/shared/QuotaField';

type Field =
  'description' | 'max_mailboxes' | 'max_aliases' | 'default_quota' | 'max_quota' | 'quota';

const FORM_ID = 'directory-domain-form';
const DESCRIPTION_MAX_LENGTH = 255;

export interface DirectoryDomainFormProps {
  domain: DirectoryDomain;
  onClose: () => void;
  onSaved: (domain: DirectoryDomain) => void;
}

export function DirectoryDomainForm({ domain, onClose, onSaved }: DirectoryDomainFormProps) {
  const { can } = useAccess();
  const canReadRelayhosts = can(...PERMISSIONS.relayhosts.read);
  const relayhosts = useQuery(
    async () =>
      canReadRelayhosts
        ? (
            await mailRoutingApi.relayhosts.list({
              page: 1,
              per_page: (await directoryMeta.get()).pagination.max_page_size,
            })
          ).items
        : [],
    [canReadRelayhosts],
  );

  const [description, setDescription] = useState(domain.description);
  const [maxMailboxes, setMaxMailboxes] = useState(String(domain.max_mailboxes));
  const [maxAliases, setMaxAliases] = useState(String(domain.max_aliases));
  const [defaultQuota, setDefaultQuota] = useState<QuotaAmount>(
    bytesToQuota(domain.default_quota_bytes),
  );
  const [maxQuota, setMaxQuota] = useState<QuotaAmount>(bytesToQuota(domain.max_quota_bytes));
  const [quota, setQuota] = useState<QuotaAmount>(bytesToQuota(domain.quota_bytes));
  const [backupMx, setBackupMx] = useState(domain.backupmx);
  const [relayAll, setRelayAll] = useState(domain.relay_all_recipients);
  const [relayUnknown, setRelayUnknown] = useState(domain.relay_unknown_only);
  const [relayhostId, setRelayhostId] = useState(domain.relayhost_id ?? '');
  const [errors, setErrors] = useState<FieldErrors<Field>>({});

  const action = useAction(async (body: UpdateDirectoryDomainRequest) => {
    const { data } = await mailDirectoryApi.updateDomain(domain.id, body);
    onSaved(data);
  });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const defaultBytes = readQuota(defaultQuota);
    const maxBytes = readQuota(maxQuota);
    const totalBytes = readQuota(quota);
    const next: FieldErrors<Field> = {
      description: validateField(description, rules.maxLength(DESCRIPTION_MAX_LENGTH)) ?? undefined,
      max_mailboxes:
        validateField(maxMailboxes, rules.required, rules.nonNegativeInteger) ?? undefined,
      max_aliases: validateField(maxAliases, rules.required, rules.nonNegativeInteger) ?? undefined,
      default_quota: defaultBytes.error ?? undefined,
      max_quota: maxBytes.error ?? undefined,
      quota: totalBytes.error ?? undefined,
    };
    // Espejo de DomainLimits.Validate: con maximo por buzon, la cuota por defecto no lo supera.
    if (
      !next.default_quota &&
      maxBytes.bytes !== undefined &&
      defaultBytes.bytes !== undefined &&
      maxBytes.bytes > 0 &&
      defaultBytes.bytes > maxBytes.bytes
    ) {
      next.default_quota = t('directory.defaultExceedsMax');
    }
    setErrors(next);
    if (hasErrors(next)) return;

    const body: UpdateDirectoryDomainRequest = {};
    if (description.trim() !== domain.description) body.description = description.trim();
    if (Number(maxMailboxes) !== domain.max_mailboxes) body.max_mailboxes = Number(maxMailboxes);
    if (Number(maxAliases) !== domain.max_aliases) body.max_aliases = Number(maxAliases);
    if (defaultBytes.bytes !== domain.default_quota_bytes) {
      body.default_quota_bytes = defaultBytes.bytes;
    }
    if (maxBytes.bytes !== domain.max_quota_bytes) body.max_quota_bytes = maxBytes.bytes;
    if (totalBytes.bytes !== domain.quota_bytes) body.quota_bytes = totalBytes.bytes;
    if (backupMx !== domain.backupmx) body.backupmx = backupMx;
    if (relayAll !== domain.relay_all_recipients) body.relay_all_recipients = relayAll;
    if (relayUnknown !== domain.relay_unknown_only) body.relay_unknown_only = relayUnknown;
    if ((relayhostId || null) !== domain.relayhost_id) body.relayhost_id = relayhostId || null;

    if (Object.keys(body).length === 0) {
      onClose();
      return;
    }
    await action.run(body);
  };

  const relayhostOptions = (relayhosts.data ?? []).map((r) => ({ value: r.id, label: r.hostname }));
  if (domain.relayhost_id && !relayhostOptions.some((o) => o.value === domain.relayhost_id)) {
    relayhostOptions.push({ value: domain.relayhost_id, label: domain.relayhost_id });
  }

  return (
    <Modal
      open
      size="lg"
      title={t('directory.form.title', { domain: domain.domain })}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose} disabled={action.busy}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" form={FORM_ID} variant="primary" loading={action.busy}>
            {t('common.save')}
          </Button>
        </>
      }
    >
      <form id={FORM_ID} className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
        <FormField
          label={t('directory.form.description')}
          htmlFor="dir-description"
          error={errors.description}
        >
          <Input
            id="dir-description"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            invalid={Boolean(errors.description)}
          />
        </FormField>
        <div className="cf-form__section">{t('directory.form.limits')}</div>
        <div className="cf-form__row">
          <FormField
            label={t('directory.column.maxMailboxes')}
            htmlFor="dir-max-mailboxes"
            required
            error={errors.max_mailboxes}
            hint={t('directory.form.zeroNoLimit')}
          >
            <Input
              id="dir-max-mailboxes"
              type="number"
              min={0}
              value={maxMailboxes}
              onChange={(e) => setMaxMailboxes(e.target.value)}
              invalid={Boolean(errors.max_mailboxes)}
            />
          </FormField>
          <FormField
            label={t('directory.column.maxAliases')}
            htmlFor="dir-max-aliases"
            required
            error={errors.max_aliases}
            hint={t('directory.form.zeroNoLimit')}
          >
            <Input
              id="dir-max-aliases"
              type="number"
              min={0}
              value={maxAliases}
              onChange={(e) => setMaxAliases(e.target.value)}
              invalid={Boolean(errors.max_aliases)}
            />
          </FormField>
        </div>
        <div className="cf-form__row">
          <QuotaField
            id="dir-default-quota"
            label={t('directory.column.defaultQuota')}
            value={defaultQuota}
            onChange={setDefaultQuota}
            error={errors.default_quota}
            hint={t('directory.form.defaultQuotaHint')}
            required
          />
          <QuotaField
            id="dir-max-quota"
            label={t('directory.column.maxQuota')}
            value={maxQuota}
            onChange={setMaxQuota}
            error={errors.max_quota}
            hint={t('quota.zeroUnlimited')}
            required
          />
          <QuotaField
            id="dir-quota"
            label={t('directory.column.totalQuota')}
            value={quota}
            onChange={setQuota}
            error={errors.quota}
            hint={t('directory.form.totalQuotaHint')}
            required
          />
        </div>
        <div className="cf-form__section">{t('directory.form.relay')}</div>
        <Checkbox
          label={t('directory.form.backupMx')}
          checked={backupMx}
          onChange={(e) => setBackupMx(e.target.checked)}
        />
        {backupMx ? (
          <div className="cf-stack" style={{ gap: 'var(--cf-space-2)' }}>
            <Checkbox
              label={t('directory.form.relayAll')}
              checked={relayAll}
              onChange={(e) => setRelayAll(e.target.checked)}
            />
            <Checkbox
              label={t('directory.form.relayUnknown')}
              checked={relayUnknown}
              onChange={(e) => setRelayUnknown(e.target.checked)}
            />
          </div>
        ) : null}
        <FormField
          label={t('directory.form.relayhost')}
          htmlFor="dir-relayhost"
          hint={
            canReadRelayhosts
              ? t('directory.form.relayhostHint')
              : t('directory.form.relayhostNoAccess')
          }
        >
          <Select
            id="dir-relayhost"
            placeholder={t('directory.form.noRelayhost')}
            options={relayhostOptions}
            value={relayhostId}
            onChange={(e) => setRelayhostId(e.target.value)}
            disabled={!canReadRelayhosts}
          />
        </FormField>
        {action.error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(action.error)}
          </div>
        ) : null}
      </form>
    </Modal>
  );
}
