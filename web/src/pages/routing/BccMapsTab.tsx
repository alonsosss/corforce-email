import { useState } from 'react';
import { directoryMeta, mailRoutingApi, type BccMap, type BccType } from '@/api/mailDirectory';
import { PERMISSIONS } from '@/access/permissions';
import { useAction } from '@/hooks/useAction';
import { useResource } from '@/hooks/useResource';
import { Badge, Checkbox, FormField, Input, Select, type Column } from '@/design/components';
import { normalizeEmail } from '@/lib/mailAddress';
import { changed, isEmptyPatch } from '@/lib/patch';
import { rules, validateField } from '@/lib/validate';
import { t, tEnum } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { ResourceGate } from '@/pages/shared/ResourceGate';
import { ResourceTab, type ResourceFormProps } from '@/pages/shared/ResourceTab';
import { ActiveBadge } from '@/pages/shared/StatusBadges';

const api = mailRoutingApi.bccMaps;

const columns: Column<BccMap>[] = [
  {
    key: 'local',
    header: t('routing.bccMaps.column.localDest'),
    render: (m) => <strong className="cf-mono">{m.local_dest}</strong>,
  },
  {
    key: 'bcc',
    header: t('routing.bccMaps.column.bccDest'),
    render: (m) => <span className="cf-mono">{m.bcc_dest}</span>,
  },
  {
    key: 'type',
    header: t('routing.bccMaps.column.type'),
    render: (m) => <Badge>{tEnum('routing.bccType', m.type)}</Badge>,
  },
  { key: 'active', header: t('common.status'), render: (m) => <ActiveBadge active={m.active} /> },
];

export function BccMapsTab() {
  return (
    <ResourceTab
      permissions={PERMISSIONS.bccMaps}
      load={api.list}
      remove={api.remove}
      columns={columns}
      Form={BccMapForm}
      texts={{
        title: t('routing.bccMaps.title'),
        description: t('routing.bccMaps.description'),
        create: t('routing.bccMaps.new'),
        empty: t('routing.bccMaps.empty'),
        created: t('routing.created'),
        updated: t('routing.updated'),
        deleted: t('routing.deleted'),
        deleteTitle: t('routing.bccMaps.delete'),
        deleteConfirm: (m) =>
          t('routing.bccMaps.deleteConfirm', { local: m.local_dest, bcc: m.bcc_dest }),
      }}
    />
  );
}

function BccMapForm(props: ResourceFormProps<BccMap>) {
  const meta = useResource(directoryMeta);
  const title = props.item ? t('routing.bccMaps.editTitle') : t('routing.bccMaps.createTitle');
  return (
    <ResourceGate resource={meta} modal={{ title, onClose: props.onClose }}>
      {(rules) => <BccMapFormBody {...props} types={rules.bcc_map_types} />}
    </ResourceGate>
  );
}

function BccMapFormBody({
  item,
  onClose,
  onSaved,
  types,
}: ResourceFormProps<BccMap> & { types: readonly BccType[] }) {
  const [localDest, setLocalDest] = useState(item?.local_dest ?? '');
  const [bccDest, setBccDest] = useState(item?.bcc_dest ?? '');
  const [type, setType] = useState<BccType | ''>(item?.type ?? '');
  const [active, setActive] = useState(item?.active ?? true);
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});

  const action = useAction(async (chosen: BccType) => {
    const bcc = normalizeEmail(bccDest) ?? bccDest.trim();
    if (item) {
      const body = {
        bcc_dest: changed(bcc, item.bcc_dest),
        type: changed(chosen, item.type),
        active: changed(active, item.active),
      };
      if (isEmptyPatch(body)) {
        onClose();
        return;
      }
      await api.update(item.id, body);
    } else {
      await api.create({
        local_dest: localDest.trim().toLowerCase(),
        bcc_dest: bcc,
        type: chosen,
        active,
      });
    }
    onSaved();
  });

  const submit = async () => {
    const next = {
      local_dest: item ? undefined : (validateField(localDest, rules.required) ?? undefined),
      bcc_dest: normalizeEmail(bccDest) ? undefined : t('validation.email'),
      type: type ? undefined : t('validation.required'),
    };
    setErrors(next);
    if (Object.values(next).some(Boolean) || !type) return;
    await action.run(type);
  };

  return (
    <FormModal
      id="bcc-map-form"
      title={item ? t('routing.bccMaps.editTitle') : t('routing.bccMaps.createTitle')}
      submitLabel={item ? t('common.save') : t('common.create')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={submit}
    >
      <FormField
        label={t('routing.bccMaps.column.localDest')}
        htmlFor="bcc-local"
        required={!item}
        error={errors.local_dest}
        hint={t('routing.bccMaps.localDestHint')}
      >
        <Input
          id="bcc-local"
          className="cf-mono"
          value={localDest}
          onChange={(e) => setLocalDest(e.target.value)}
          disabled={Boolean(item)}
          invalid={Boolean(errors.local_dest)}
          autoComplete="off"
        />
      </FormField>
      <FormField
        label={t('routing.bccMaps.column.bccDest')}
        htmlFor="bcc-dest"
        required
        error={errors.bcc_dest}
      >
        <Input
          id="bcc-dest"
          className="cf-mono"
          value={bccDest}
          onChange={(e) => setBccDest(e.target.value)}
          invalid={Boolean(errors.bcc_dest)}
          autoComplete="off"
        />
      </FormField>
      <FormField
        label={t('routing.bccMaps.column.type')}
        htmlFor="bcc-type"
        required
        error={errors.type}
        hint={type ? tEnum('routing.bccTypeHint', type) : undefined}
      >
        <Select
          id="bcc-type"
          placeholder={t('common.select')}
          options={types.map((value) => ({ value, label: tEnum('routing.bccType', value) }))}
          value={type}
          onChange={(e) => setType(e.target.value as BccType | '')}
          invalid={Boolean(errors.type)}
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
