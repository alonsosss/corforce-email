import { useState } from 'react';
import { mailRoutingApi, type RecipientMap } from '@/api/mailDirectory';
import { PERMISSIONS } from '@/access/permissions';
import { useAction } from '@/hooks/useAction';
import { Checkbox, FormField, Input, type Column } from '@/design/components';
import { changed, isEmptyPatch } from '@/lib/patch';
import { rules, validateField } from '@/lib/validate';
import { t } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { ResourceTab, type ResourceFormProps } from '@/pages/shared/ResourceTab';
import { ActiveBadge } from '@/pages/shared/StatusBadges';

const api = mailRoutingApi.recipientMaps;

const columns: Column<RecipientMap>[] = [
  {
    key: 'old',
    header: t('routing.recipientMaps.column.oldDest'),
    render: (m) => <strong className="cf-mono">{m.old_dest}</strong>,
  },
  {
    key: 'new',
    header: t('routing.recipientMaps.column.newDest'),
    render: (m) => <span className="cf-mono">{m.new_dest}</span>,
  },
  { key: 'active', header: t('common.status'), render: (m) => <ActiveBadge active={m.active} /> },
];

export function RecipientMapsTab() {
  return (
    <ResourceTab
      permissions={PERMISSIONS.recipientMaps}
      load={api.list}
      remove={api.remove}
      columns={columns}
      Form={RecipientMapForm}
      texts={{
        title: t('routing.recipientMaps.title'),
        description: t('routing.recipientMaps.description'),
        create: t('routing.recipientMaps.new'),
        empty: t('routing.recipientMaps.empty'),
        created: t('routing.created'),
        updated: t('routing.updated'),
        deleted: t('routing.deleted'),
        deleteTitle: t('routing.recipientMaps.delete'),
        deleteConfirm: (m) =>
          t('routing.recipientMaps.deleteConfirm', { from: m.old_dest, to: m.new_dest }),
      }}
    />
  );
}

function RecipientMapForm({ item, onClose, onSaved }: ResourceFormProps<RecipientMap>) {
  const [oldDest, setOldDest] = useState(item?.old_dest ?? '');
  const [newDest, setNewDest] = useState(item?.new_dest ?? '');
  const [active, setActive] = useState(item?.active ?? true);
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});

  const action = useAction(async () => {
    if (item) {
      const body = {
        new_dest: changed(newDest.trim(), item.new_dest),
        active: changed(active, item.active),
      };
      if (isEmptyPatch(body)) {
        onClose();
        return;
      }
      await api.update(item.id, body);
    } else {
      await api.create({ old_dest: oldDest.trim(), new_dest: newDest.trim(), active });
    }
    onSaved();
  });

  const submit = async () => {
    const next = {
      old_dest: item ? undefined : (validateField(oldDest, rules.required) ?? undefined),
      new_dest: validateField(newDest, rules.required) ?? undefined,
    };
    setErrors(next);
    if (next.old_dest || next.new_dest) return;
    await action.run();
  };

  return (
    <FormModal
      id="recipient-map-form"
      title={item ? t('routing.recipientMaps.editTitle') : t('routing.recipientMaps.createTitle')}
      submitLabel={item ? t('common.save') : t('common.create')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={submit}
    >
      <FormField
        label={t('routing.recipientMaps.column.oldDest')}
        htmlFor="rmap-old"
        required={!item}
        error={errors.old_dest}
        hint={t('routing.recipientMaps.destHint')}
      >
        <Input
          id="rmap-old"
          className="cf-mono"
          value={oldDest}
          onChange={(e) => setOldDest(e.target.value)}
          disabled={Boolean(item)}
          invalid={Boolean(errors.old_dest)}
          autoComplete="off"
        />
      </FormField>
      <FormField
        label={t('routing.recipientMaps.column.newDest')}
        htmlFor="rmap-new"
        required
        error={errors.new_dest}
        hint={t('routing.recipientMaps.destHint')}
      >
        <Input
          id="rmap-new"
          className="cf-mono"
          value={newDest}
          onChange={(e) => setNewDest(e.target.value)}
          invalid={Boolean(errors.new_dest)}
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
