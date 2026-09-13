import { useState, type FormEvent } from 'react';
import {
  DMARC_POLICIES,
  DOMAIN_PURPOSES,
  domainsApi,
  type CreateDomainRequest,
  type DmarcPolicy,
  type DomainPurpose,
  type DomainWithRecords,
} from '@/api/domains';
import { ERROR_CODES } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import { useAction } from '@/hooks/useAction';
import { Button, FormField, Input, Modal, Select } from '@/design/components';
import { normalizeDomainName } from '@/lib/mailAddress';
import { hasErrors, rules, validateField, type FieldErrors } from '@/lib/validate';
import { t, tEnum } from '@/i18n';

type Field = 'domain' | 'purpose';

const FORM_ID = 'domain-create-form';

export interface DomainCreateFormProps {
  onClose: () => void;
  onCreated: (domain: DomainWithRecords) => void;
}

export function DomainCreateForm({ onClose, onCreated }: DomainCreateFormProps) {
  const [domain, setDomain] = useState('');
  const [purpose, setPurpose] = useState<DomainPurpose | ''>('');
  const [dmarc, setDmarc] = useState<DmarcPolicy | ''>('');
  const [errors, setErrors] = useState<FieldErrors<Field>>({});

  const action = useAction(async (input: CreateDomainRequest) => {
    const { data } = await domainsApi.create(input);
    onCreated(data);
  });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const name = normalizeDomainName(domain);
    const domainError =
      validateField(domain, rules.required) ?? (name ? null : t('validation.domain'));
    const next: FieldErrors<Field> = {
      domain: domainError ?? undefined,
      purpose: purpose ? undefined : t('validation.required'),
    };
    setErrors(next);
    if (hasErrors(next) || !name || !purpose) return;
    await action.run({ domain: name, purpose, dmarc_policy: dmarc || undefined });
  };

  return (
    <Modal
      open
      title={t('domains.form.createTitle')}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose} disabled={action.busy}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" form={FORM_ID} variant="primary" loading={action.busy}>
            {t('common.create')}
          </Button>
        </>
      }
    >
      <form id={FORM_ID} className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
        <FormField
          label={t('domains.form.domain')}
          htmlFor="domain-name"
          required
          error={errors.domain}
          hint={t('domains.form.domainHint')}
        >
          <Input
            id="domain-name"
            className="cf-mono"
            value={domain}
            onChange={(e) => setDomain(e.target.value)}
            invalid={Boolean(errors.domain)}
            autoComplete="off"
            spellCheck={false}
          />
        </FormField>
        <FormField
          label={t('domains.form.purpose')}
          htmlFor="domain-purpose"
          required
          error={errors.purpose}
          hint={purpose ? tEnum('domains.purposeHint', purpose) : undefined}
        >
          <Select
            id="domain-purpose"
            placeholder={t('common.select')}
            options={DOMAIN_PURPOSES.map((p) => ({ value: p, label: tEnum('domains.purpose', p) }))}
            value={purpose}
            onChange={(e) => setPurpose(e.target.value as DomainPurpose | '')}
            invalid={Boolean(errors.purpose)}
          />
        </FormField>
        <FormField
          label={t('domains.form.dmarc')}
          htmlFor="domain-dmarc"
          hint={dmarc ? tEnum('domains.dmarcHint', dmarc) : t('domains.form.dmarcDefault')}
        >
          <Select
            id="domain-dmarc"
            placeholder={t('domains.form.dmarcServerDefault')}
            options={DMARC_POLICIES.map((p) => ({ value: p, label: tEnum('domains.dmarc', p) }))}
            value={dmarc}
            onChange={(e) => setDmarc(e.target.value as DmarcPolicy | '')}
          />
        </FormField>
        {action.error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(action.error, { [ERROR_CODES.CONFLICT]: 'domains.exists' })}
          </div>
        ) : null}
      </form>
    </Modal>
  );
}
