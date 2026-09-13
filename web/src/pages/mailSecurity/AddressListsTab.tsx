import { useCallback, useState, type FormEvent } from 'react';
import {
  LIST_KINDS,
  mailSecurityApi,
  type AddressListEntry,
  type ListKind,
} from '@/api/mailSecurity';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { Badge, Button, FormField, Input, Select, type Column } from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { t, tEnum } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { ListTab, type ResourceFormProps } from '@/pages/shared/ResourceTab';
import { ObjectField } from './ObjectField';
import { normalizeListPattern, normalizePolicyObject } from './policyObject';

interface Filters {
  object: string;
  kind: ListKind | '';
}

const EMPTY: Filters = { object: '', kind: '' };

const columns: Column<AddressListEntry>[] = [
  {
    key: 'object',
    header: t('security.object'),
    render: (e) => <span className="cf-mono">{e.object}</span>,
  },
  {
    key: 'kind',
    header: t('security.addressLists.kind'),
    render: (e) => (
      <Badge tone={e.kind === 'allow' ? 'success' : 'danger'}>
        {tEnum('security.listKind', e.kind)}
      </Badge>
    ),
  },
  {
    key: 'pattern',
    header: t('security.addressLists.pattern'),
    render: (e) => <strong className="cf-mono">{e.pattern}</strong>,
  },
  { key: 'created', header: t('common.createdAt'), render: (e) => formatDateTime(e.created_at) },
];

export function AddressListsTab() {
  const { can } = useAccess();
  const [draft, setDraft] = useState<Filters>(EMPTY);
  const [applied, setApplied] = useState<Filters>(EMPTY);

  const load = useCallback(
    () =>
      mailSecurityApi.listAddressLists({
        object: applied.object.trim().toLowerCase() || undefined,
        kind: applied.kind || undefined,
      }),
    [applied],
  );

  const apply = (e: FormEvent) => {
    e.preventDefault();
    setApplied(draft);
  };

  return (
    <ListTab
      load={load}
      rowKey={(e) => e.id}
      columns={columns}
      Form={AddressListForm}
      canCreate={can(...PERMISSIONS.addressLists.create)}
      canUpdate={false}
      canDelete={can(...PERMISSIONS.addressLists.delete)}
      remove={(e) => mailSecurityApi.deleteAddressList(e.id)}
      toolbar={
        <form className="cf-toolbar" onSubmit={apply}>
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="lists-object">
              {t('security.object')}
            </label>
            <Input
              id="lists-object"
              className="cf-mono"
              value={draft.object}
              onChange={(e) => setDraft((d) => ({ ...d, object: e.target.value }))}
            />
          </div>
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="lists-kind">
              {t('security.addressLists.kind')}
            </label>
            <Select
              id="lists-kind"
              placeholder={t('common.all')}
              options={LIST_KINDS.map((k) => ({ value: k, label: tEnum('security.listKind', k) }))}
              value={draft.kind}
              onChange={(e) => setDraft((d) => ({ ...d, kind: e.target.value as ListKind | '' }))}
            />
          </div>
          <div className="cf-toolbar__actions">
            <Button
              onClick={() => {
                setDraft(EMPTY);
                setApplied(EMPTY);
              }}
            >
              {t('common.clear')}
            </Button>
            <Button type="submit" variant="primary">
              {t('common.apply')}
            </Button>
          </div>
        </form>
      }
      texts={{
        title: t('security.addressLists.title'),
        description: t('security.addressLists.description'),
        create: t('security.addressLists.new'),
        empty: t('security.addressLists.empty'),
        created: t('security.saved'),
        updated: t('security.saved'),
        deleted: t('security.deleted'),
        deleteTitle: t('security.addressLists.delete'),
        deleteConfirm: (e) =>
          t('security.addressLists.deleteConfirm', { pattern: e.pattern, object: e.object }),
      }}
    />
  );
}

function AddressListForm({ onClose, onSaved }: ResourceFormProps<AddressListEntry>) {
  const [object, setObject] = useState('');
  const [kind, setKind] = useState<ListKind | ''>('');
  const [pattern, setPattern] = useState('');
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});

  const action = useAction(async (input: { object: string; kind: ListKind; pattern: string }) => {
    await mailSecurityApi.createAddressList(input);
    onSaved();
  });

  const submit = async () => {
    const target = normalizePolicyObject(object);
    const normalizedPattern = normalizeListPattern(pattern);
    const next = {
      object: target ? undefined : t('security.objectInvalid'),
      kind: kind ? undefined : t('validation.required'),
      pattern: normalizedPattern ? undefined : t('security.addressLists.patternInvalid'),
    };
    setErrors(next);
    if (!target || !kind || !normalizedPattern) return;
    await action.run({ object: target, kind, pattern: normalizedPattern });
  };

  return (
    <FormModal
      id="address-list-form"
      title={t('security.addressLists.createTitle')}
      submitLabel={t('common.create')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={submit}
    >
      <ObjectField
        id="address-list-object"
        value={object}
        onChange={setObject}
        error={errors.object}
      />
      <FormField
        label={t('security.addressLists.kind')}
        htmlFor="address-list-kind"
        required
        error={errors.kind}
      >
        <Select
          id="address-list-kind"
          placeholder={t('common.select')}
          options={LIST_KINDS.map((k) => ({ value: k, label: tEnum('security.listKind', k) }))}
          value={kind}
          onChange={(e) => setKind(e.target.value as ListKind | '')}
          invalid={Boolean(errors.kind)}
        />
      </FormField>
      <FormField
        label={t('security.addressLists.pattern')}
        htmlFor="address-list-pattern"
        required
        error={errors.pattern}
        hint={t('security.addressLists.patternHint')}
      >
        <Input
          id="address-list-pattern"
          className="cf-mono"
          value={pattern}
          onChange={(e) => setPattern(e.target.value)}
          invalid={Boolean(errors.pattern)}
          autoComplete="off"
        />
      </FormField>
    </FormModal>
  );
}
