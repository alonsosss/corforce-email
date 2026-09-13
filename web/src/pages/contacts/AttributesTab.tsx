import { useState } from 'react';
import {
  contactsApi,
  contactsMeta,
  type AttributeDefinition,
  type AttributeType,
} from '@/api/contacts';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { useResource } from '@/hooks/useResource';
import { Checkbox, FormField, Input, Select, type Column } from '@/design/components';
import { rules, validateField } from '@/lib/validate';
import { t, tEnum } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { ResourceGate } from '@/pages/shared/ResourceGate';
import { ListTab, type ResourceFormProps } from '@/pages/shared/ResourceTab';
import { YesNo } from '@/pages/shared/StatusBadges';

const loadAttributes = () => contactsApi.listAttributes();

const columns: Column<AttributeDefinition>[] = [
  {
    key: 'key',
    header: t('contacts.attributes.key'),
    render: (a) => <code className="cf-mono">{a.key}</code>,
  },
  {
    key: 'label',
    header: t('contacts.attributes.label'),
    render: (a) => a.label || t('common.dash'),
  },
  {
    key: 'type',
    header: t('contacts.attributes.type'),
    render: (a) => tEnum('contacts.attributeType', a.type),
  },
  {
    key: 'required',
    header: t('contacts.attributes.required'),
    render: (a) => <YesNo value={a.required} />,
  },
];

export function AttributesTab() {
  const { can } = useAccess();
  return (
    <ListTab<AttributeDefinition>
      load={loadAttributes}
      rowKey={(a) => a.key}
      columns={columns}
      Form={AttributeForm}
      canCreate={can(...PERMISSIONS.contactAttributes.create)}
      canUpdate={can(...PERMISSIONS.contactAttributes.update)}
      canDelete={can(...PERMISSIONS.contactAttributes.delete)}
      remove={(a) => contactsApi.deleteAttribute(a.key)}
      texts={{
        title: t('contacts.attributes.title'),
        description: t('contacts.attributes.description'),
        create: t('contacts.attributes.new'),
        empty: t('contacts.attributes.empty'),
        created: t('contacts.attributes.created'),
        updated: t('contacts.attributes.updated'),
        deleted: t('contacts.attributes.deleted'),
        deleteTitle: t('contacts.attributes.delete'),
        deleteConfirm: (a) => t('contacts.attributes.deleteConfirm', { key: a.key }),
      }}
    />
  );
}

function AttributeForm(props: ResourceFormProps<AttributeDefinition>) {
  const meta = useResource(contactsMeta);
  const title = props.item
    ? t('contacts.attributes.editTitle')
    : t('contacts.attributes.createTitle');
  return (
    <ResourceGate resource={meta} modal={{ title, onClose: props.onClose }}>
      {(catalog) => <AttributeFormBody {...props} types={catalog.attribute_types} />}
    </ResourceGate>
  );
}

function AttributeFormBody({
  item,
  onClose,
  onSaved,
  types,
}: ResourceFormProps<AttributeDefinition> & { types: readonly AttributeType[] }) {
  const [key, setKey] = useState(item?.key ?? '');
  const [type, setType] = useState<AttributeType | ''>(item?.type ?? '');
  const [label, setLabel] = useState(item?.label ?? '');
  const [required, setRequired] = useState(item?.required ?? false);
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});

  const action = useAction(async () => {
    if (item) {
      await contactsApi.updateAttribute(item.key, { label: label.trim(), required });
    } else if (type) {
      await contactsApi.createAttribute({ key: key.trim(), type, label: label.trim(), required });
    }
    onSaved();
  });

  const submit = async () => {
    const next = item
      ? {}
      : {
          key: validateField(key, rules.required) ?? undefined,
          type: type ? undefined : t('validation.required'),
        };
    setErrors(next);
    if (Object.values(next).some(Boolean)) return;
    await action.run();
  };

  return (
    <FormModal
      id="contact-attribute-form"
      title={item ? t('contacts.attributes.editTitle') : t('contacts.attributes.createTitle')}
      submitLabel={item ? t('common.save') : t('common.create')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={submit}
    >
      <div className="cf-form__row">
        <FormField
          label={t('contacts.attributes.key')}
          htmlFor="attribute-key"
          required={!item}
          error={errors.key}
          hint={item ? t('contacts.attributes.immutable') : t('contacts.attributes.keyHint')}
        >
          <Input
            id="attribute-key"
            className="cf-mono"
            value={key}
            onChange={(e) => setKey(e.target.value)}
            readOnly={Boolean(item)}
            invalid={Boolean(errors.key)}
            spellCheck={false}
          />
        </FormField>
        <FormField
          label={t('contacts.attributes.type')}
          htmlFor="attribute-type"
          required={!item}
          error={errors.type}
        >
          <Select
            id="attribute-type"
            placeholder={t('common.select')}
            options={types.map((v) => ({
              value: v,
              label: tEnum('contacts.attributeType', v),
            }))}
            value={type}
            onChange={(e) => setType(e.target.value as AttributeType | '')}
            disabled={Boolean(item)}
            invalid={Boolean(errors.type)}
          />
        </FormField>
      </div>
      <FormField label={t('contacts.attributes.label')} htmlFor="attribute-label">
        <Input id="attribute-label" value={label} onChange={(e) => setLabel(e.target.value)} />
      </FormField>
      <Checkbox
        label={t('contacts.attributes.requiredLabel')}
        checked={required}
        onChange={(e) => setRequired(e.target.checked)}
      />
    </FormModal>
  );
}
