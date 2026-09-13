import { useState } from 'react';
import { mailRoutingApi, type SpamAlias } from '@/api/mailDirectory';
import { PERMISSIONS } from '@/access/permissions';
import { useAction } from '@/hooks/useAction';
import { Badge, Checkbox, FormField, Input, type Column } from '@/design/components';
import { isPast, toDatetimeLocal } from '@/lib/datetime';
import { formatDateTime, localToRfc3339 } from '@/lib/format';
import { normalizeEmail } from '@/lib/mailAddress';
import { changed, isEmptyPatch } from '@/lib/patch';
import { rules, validateField } from '@/lib/validate';
import { t } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { ResourceTab, type ResourceFormProps } from '@/pages/shared/ResourceTab';

const api = mailRoutingApi.spamAliases;
const DESCRIPTION_MAX_LENGTH = 255;

function Validity({ alias }: { alias: SpamAlias }) {
  if (alias.permanent) return <Badge tone="info">{t('routing.spamAliases.permanent')}</Badge>;
  return (
    <span className="cf-inline">
      {formatDateTime(alias.valid_until)}
      {isPast(alias.valid_until) ? (
        <Badge tone="danger">{t('routing.spamAliases.expired')}</Badge>
      ) : null}
    </span>
  );
}

const columns: Column<SpamAlias>[] = [
  {
    key: 'address',
    header: t('routing.aliases.column.address'),
    render: (a) => <strong className="cf-mono">{a.address}</strong>,
  },
  {
    key: 'goto',
    header: t('routing.spamAliases.column.goto'),
    render: (a) => <span className="cf-mono">{a.goto}</span>,
  },
  {
    key: 'description',
    header: t('common.description'),
    render: (a) => a.description || t('common.dash'),
  },
  {
    key: 'validity',
    header: t('routing.spamAliases.column.validity'),
    render: (a) => <Validity alias={a} />,
  },
];

export function SpamAliasesTab() {
  return (
    <ResourceTab
      permissions={PERMISSIONS.spamAliases}
      load={api.list}
      remove={api.remove}
      columns={columns}
      Form={SpamAliasForm}
      texts={{
        title: t('routing.spamAliases.title'),
        description: t('routing.spamAliases.description'),
        create: t('routing.spamAliases.new'),
        empty: t('routing.spamAliases.empty'),
        created: t('routing.created'),
        updated: t('routing.updated'),
        deleted: t('routing.deleted'),
        deleteTitle: t('routing.spamAliases.delete'),
        deleteConfirm: (a) => t('routing.aliases.deleteConfirm', { address: a.address }),
      }}
    />
  );
}

function SpamAliasForm({ item, onClose, onSaved }: ResourceFormProps<SpamAlias>) {
  const [address, setAddress] = useState(item?.address ?? '');
  const [goto, setGoto] = useState(item?.goto ?? '');
  const [description, setDescription] = useState(item?.description ?? '');
  const [permanent, setPermanent] = useState(item?.permanent ?? false);
  const [validUntil, setValidUntil] = useState(toDatetimeLocal(item?.valid_until));
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});

  const action = useAction(async () => {
    const until = permanent ? undefined : localToRfc3339(validUntil);
    if (item) {
      const body = {
        description: changed(description.trim(), item.description),
        permanent: changed(permanent, item.permanent),
        valid_until: until && until !== item.valid_until ? until : undefined,
      };
      if (isEmptyPatch(body)) {
        onClose();
        return;
      }
      await api.update(item.id, body);
    } else {
      await api.create({
        address: normalizeEmail(address) ?? address,
        goto: normalizeEmail(goto) ?? goto,
        description: description.trim(),
        permanent,
        valid_until: until,
      });
    }
    onSaved();
  });

  const submit = async () => {
    const next = {
      address: item || normalizeEmail(address) ? undefined : t('validation.email'),
      goto: item || normalizeEmail(goto) ? undefined : t('validation.email'),
      description: validateField(description, rules.maxLength(DESCRIPTION_MAX_LENGTH)) ?? undefined,
      valid_until:
        permanent || localToRfc3339(validUntil)
          ? undefined
          : t('routing.spamAliases.validityRequired'),
    };
    setErrors(next);
    if (Object.values(next).some(Boolean)) return;
    await action.run();
  };

  return (
    <FormModal
      id="spam-alias-form"
      title={item ? t('routing.spamAliases.editTitle') : t('routing.spamAliases.createTitle')}
      submitLabel={item ? t('common.save') : t('common.create')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={submit}
    >
      <FormField
        label={t('routing.aliases.column.address')}
        htmlFor="spam-alias-address"
        required={!item}
        error={errors.address}
      >
        <Input
          id="spam-alias-address"
          className="cf-mono"
          value={address}
          onChange={(e) => setAddress(e.target.value)}
          disabled={Boolean(item)}
          invalid={Boolean(errors.address)}
          autoComplete="off"
        />
      </FormField>
      <FormField
        label={t('routing.spamAliases.column.goto')}
        htmlFor="spam-alias-goto"
        required={!item}
        error={errors.goto}
      >
        <Input
          id="spam-alias-goto"
          className="cf-mono"
          value={goto}
          onChange={(e) => setGoto(e.target.value)}
          disabled={Boolean(item)}
          invalid={Boolean(errors.goto)}
          autoComplete="off"
        />
      </FormField>
      <FormField
        label={t('common.description')}
        htmlFor="spam-alias-desc"
        error={errors.description}
      >
        <Input
          id="spam-alias-desc"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
      </FormField>
      <Checkbox
        label={t('routing.spamAliases.permanentLabel')}
        checked={permanent}
        onChange={(e) => setPermanent(e.target.checked)}
      />
      {permanent ? null : (
        <FormField
          label={t('routing.spamAliases.column.validity')}
          htmlFor="spam-alias-until"
          required
          error={errors.valid_until}
        >
          <Input
            id="spam-alias-until"
            type="datetime-local"
            value={validUntil}
            onChange={(e) => setValidUntil(e.target.value)}
            invalid={Boolean(errors.valid_until)}
          />
        </FormField>
      )}
    </FormModal>
  );
}
