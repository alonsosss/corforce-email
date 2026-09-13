import { useState, type FormEvent } from 'react';
import { mailDirectoryApi, type AliasDomain } from '@/api/mailDirectory';
import { errorMessage } from '@/api/messages';
import { PERMISSIONS } from '@/access/permissions';
import { useAction } from '@/hooks/useAction';
import {
  Button,
  Checkbox,
  FormField,
  Input,
  Modal,
  Select,
  type Column,
} from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { normalizeDomainName } from '@/lib/mailAddress';
import { t } from '@/i18n';
import { ResourceTab, type ResourceFormProps } from '@/pages/shared/ResourceTab';
import { ActiveBadge } from '@/pages/shared/StatusBadges';
import { useDirectoryDomains } from '@/pages/shared/useDirectoryDomains';

const api = mailDirectoryApi.aliasDomains;

const columns: Column<AliasDomain>[] = [
  {
    key: 'alias',
    header: t('aliasDomains.column.alias'),
    render: (a) => <strong className="cf-mono">{a.alias_domain}</strong>,
  },
  {
    key: 'target',
    header: t('aliasDomains.column.target'),
    render: (a) => <span className="cf-mono">{a.target_domain}</span>,
  },
  { key: 'active', header: t('common.status'), render: (a) => <ActiveBadge active={a.active} /> },
  { key: 'updated', header: t('common.updatedAt'), render: (a) => formatDateTime(a.updated_at) },
];

/** Dominios alias: el correo a alias.com llega a los buzones homonimos de destino.com. */
export function AliasDomainsTab() {
  return (
    <ResourceTab
      permissions={PERMISSIONS.aliasDomains}
      load={api.list}
      remove={api.remove}
      columns={columns}
      Form={AliasDomainForm}
      texts={{
        title: t('aliasDomains.title'),
        description: t('aliasDomains.description'),
        create: t('aliasDomains.new'),
        empty: t('aliasDomains.empty'),
        created: t('aliasDomains.created'),
        updated: t('aliasDomains.updated'),
        deleted: t('aliasDomains.deleted'),
        deleteTitle: t('aliasDomains.delete'),
        deleteConfirm: (a) => t('aliasDomains.deleteConfirm', { domain: a.alias_domain }),
      }}
    />
  );
}

const FORM_ID = 'alias-domain-form';

function AliasDomainForm({ item, onClose, onSaved }: ResourceFormProps<AliasDomain>) {
  const targets = useDirectoryDomains();
  const [alias, setAlias] = useState(item?.alias_domain ?? '');
  const [target, setTarget] = useState(item?.target_domain ?? '');
  const [active, setActive] = useState(item?.active ?? true);
  const [errors, setErrors] = useState<{ alias?: string; target?: string }>({});

  const action = useAction(async () => {
    if (item) {
      const body = {
        target_domain: target !== item.target_domain ? target : undefined,
        active: active !== item.active ? active : undefined,
      };
      if (body.target_domain === undefined && body.active === undefined) {
        onClose();
        return;
      }
      await api.update(item.id, body);
    } else {
      await api.create({
        alias_domain: normalizeDomainName(alias) ?? alias,
        target_domain: target,
        active,
      });
    }
    onSaved();
  });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const next = {
      alias: item || normalizeDomainName(alias) ? undefined : t('validation.domain'),
      target: target ? undefined : t('validation.required'),
    };
    setErrors(next);
    if (next.alias || next.target) return;
    await action.run();
  };

  const targetOptions = (targets.data ?? []).map((d) => ({ value: d.domain, label: d.domain }));
  if (target && !targetOptions.some((o) => o.value === target)) {
    targetOptions.push({ value: target, label: target });
  }

  return (
    <Modal
      open
      title={item ? t('aliasDomains.form.editTitle') : t('aliasDomains.form.createTitle')}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose} disabled={action.busy}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" form={FORM_ID} variant="primary" loading={action.busy}>
            {item ? t('common.save') : t('common.create')}
          </Button>
        </>
      }
    >
      <form id={FORM_ID} className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
        <FormField
          label={t('aliasDomains.column.alias')}
          htmlFor="alias-domain-name"
          required={!item}
          error={errors.alias}
          hint={t('aliasDomains.form.aliasHint')}
        >
          <Input
            id="alias-domain-name"
            className="cf-mono"
            value={alias}
            onChange={(e) => setAlias(e.target.value)}
            invalid={Boolean(errors.alias)}
            disabled={Boolean(item)}
            autoComplete="off"
          />
        </FormField>
        <FormField
          label={t('aliasDomains.column.target')}
          htmlFor="alias-domain-target"
          required
          error={errors.target ?? (targets.error ? errorMessage(targets.error) : undefined)}
        >
          <Select
            id="alias-domain-target"
            placeholder={t('common.select')}
            options={targetOptions}
            value={target}
            onChange={(e) => setTarget(e.target.value)}
            invalid={Boolean(errors.target)}
          />
        </FormField>
        <Checkbox
          label={t('common.active')}
          checked={active}
          onChange={(e) => setActive(e.target.checked)}
        />
        {action.error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(action.error)}
          </div>
        ) : null}
      </form>
    </Modal>
  );
}
