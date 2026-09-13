import { useState } from 'react';
import {
  TLS_POLICIES,
  mailRoutingApi,
  type TlsPolicy,
  type TlsPolicyName,
} from '@/api/mailDirectory';
import { PERMISSIONS } from '@/access/permissions';
import { useAction } from '@/hooks/useAction';
import { Badge, Checkbox, FormField, Input, Select, type Column } from '@/design/components';
import { changed, isEmptyPatch } from '@/lib/patch';
import { rules, validateField } from '@/lib/validate';
import { t, tEnum } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { ResourceTab, type ResourceFormProps } from '@/pages/shared/ResourceTab';
import { ActiveBadge } from '@/pages/shared/StatusBadges';

const api = mailRoutingApi.tlsPolicies;
const PARAMETERS_MAX_LENGTH = 255;

const columns: Column<TlsPolicy>[] = [
  {
    key: 'dest',
    header: t('routing.tlsPolicies.column.dest'),
    render: (p) => <strong className="cf-mono">{p.dest}</strong>,
  },
  {
    key: 'policy',
    header: t('routing.tlsPolicies.column.policy'),
    render: (p) => <Badge tone="info">{p.policy}</Badge>,
  },
  {
    key: 'parameters',
    header: t('routing.tlsPolicies.column.parameters'),
    render: (p) =>
      p.parameters ? <span className="cf-mono">{p.parameters}</span> : t('common.dash'),
  },
  { key: 'active', header: t('common.status'), render: (p) => <ActiveBadge active={p.active} /> },
];

export function TlsPoliciesTab() {
  return (
    <ResourceTab
      permissions={PERMISSIONS.tlsPolicies}
      load={api.list}
      remove={api.remove}
      columns={columns}
      Form={TlsPolicyForm}
      texts={{
        title: t('routing.tlsPolicies.title'),
        description: t('routing.tlsPolicies.description'),
        create: t('routing.tlsPolicies.new'),
        empty: t('routing.tlsPolicies.empty'),
        created: t('routing.created'),
        updated: t('routing.updated'),
        deleted: t('routing.deleted'),
        deleteTitle: t('routing.tlsPolicies.delete'),
        deleteConfirm: (p) => t('routing.tlsPolicies.deleteConfirm', { dest: p.dest }),
      }}
    />
  );
}

function TlsPolicyForm({ item, onClose, onSaved }: ResourceFormProps<TlsPolicy>) {
  const [dest, setDest] = useState(item?.dest ?? '');
  const [policy, setPolicy] = useState<TlsPolicyName | ''>(item?.policy ?? '');
  const [parameters, setParameters] = useState(item?.parameters ?? '');
  const [active, setActive] = useState(item?.active ?? true);
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});

  const action = useAction(async (chosen: TlsPolicyName) => {
    if (item) {
      const body = {
        policy: changed(chosen, item.policy),
        parameters: changed(parameters.trim(), item.parameters),
        active: changed(active, item.active),
      };
      if (isEmptyPatch(body)) {
        onClose();
        return;
      }
      await api.update(item.id, body);
    } else {
      await api.create({
        dest: dest.trim().toLowerCase(),
        policy: chosen,
        parameters: parameters.trim(),
        active,
      });
    }
    onSaved();
  });

  const submit = async () => {
    const next = {
      dest: validateField(dest, rules.required) ?? undefined,
      policy: policy ? undefined : t('validation.required'),
      parameters: validateField(parameters, rules.maxLength(PARAMETERS_MAX_LENGTH)) ?? undefined,
    };
    setErrors(next);
    if (Object.values(next).some(Boolean) || !policy) return;
    await action.run(policy);
  };

  return (
    <FormModal
      id="tls-policy-form"
      title={item ? t('routing.tlsPolicies.editTitle') : t('routing.tlsPolicies.createTitle')}
      submitLabel={item ? t('common.save') : t('common.create')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={submit}
    >
      <FormField
        label={t('routing.tlsPolicies.column.dest')}
        htmlFor="tls-dest"
        required={!item}
        error={errors.dest}
        hint={t('routing.tlsPolicies.destHint')}
      >
        <Input
          id="tls-dest"
          className="cf-mono"
          value={dest}
          onChange={(e) => setDest(e.target.value)}
          disabled={Boolean(item)}
          invalid={Boolean(errors.dest)}
          autoComplete="off"
        />
      </FormField>
      <FormField
        label={t('routing.tlsPolicies.column.policy')}
        htmlFor="tls-policy"
        required
        error={errors.policy}
        hint={policy ? tEnum('routing.tlsPolicy', policy) : undefined}
      >
        <Select
          id="tls-policy"
          placeholder={t('common.select')}
          options={TLS_POLICIES.map((p) => ({ value: p, label: p }))}
          value={policy}
          onChange={(e) => setPolicy(e.target.value as TlsPolicyName | '')}
          invalid={Boolean(errors.policy)}
        />
      </FormField>
      <FormField
        label={t('routing.tlsPolicies.column.parameters')}
        htmlFor="tls-parameters"
        error={errors.parameters}
        hint={t('routing.tlsPolicies.parametersHint')}
      >
        <Input
          id="tls-parameters"
          className="cf-mono"
          value={parameters}
          onChange={(e) => setParameters(e.target.value)}
          autoComplete="off"
        />
      </FormField>
      <Checkbox
        label={t('common.active')}
        checked={active}
        onChange={(e) => setActive(e.target.checked)}
      />
    </FormModal>
  );
}
