import { useState } from 'react';
import { mailRoutingApi, type ActiveState, type MailAlias } from '@/api/mailDirectory';
import { PERMISSIONS } from '@/access/permissions';
import { useAction } from '@/hooks/useAction';
import { Badge, Checkbox, ChipsInput, FormField, Input, type Column } from '@/design/components';
import { gotoToList, listToGoto, normalizeAliasAddress, normalizeEmail } from '@/lib/mailAddress';
import { changed, isEmptyPatch } from '@/lib/patch';
import { rules, validateField } from '@/lib/validate';
import { t } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { ResourceTab, type ResourceFormProps } from '@/pages/shared/ResourceTab';
import { ActiveStateBadge, YesNo } from '@/pages/shared/StatusBadges';

const api = mailRoutingApi.aliases;
const COMMENT_MAX_LENGTH = 1000;
const GOTO_PREVIEW = 3;

const columns: Column<MailAlias>[] = [
  {
    key: 'address',
    header: t('routing.aliases.column.address'),
    render: (a) => (
      <div className="cf-cell-stack">
        <strong className="cf-mono">{a.address}</strong>
        {a.public_comment ? (
          <span className="cf-text-muted cf-text-sm">{a.public_comment}</span>
        ) : null}
      </div>
    ),
  },
  {
    key: 'goto',
    header: t('routing.aliases.column.goto'),
    render: (a) => {
      const targets = gotoToList(a.goto);
      return (
        <span className="cf-inline-list">
          {targets.slice(0, GOTO_PREVIEW).map((target) => (
            <Badge key={target}>{target}</Badge>
          ))}
          {targets.length > GOTO_PREVIEW ? (
            <Badge tone="info">
              {t('routing.aliases.more', { n: targets.length - GOTO_PREVIEW })}
            </Badge>
          ) : null}
        </span>
      );
    },
  },
  {
    key: 'sender',
    header: t('routing.aliases.column.senderAllowed'),
    render: (a) => <YesNo value={a.sender_allowed} />,
  },
  {
    key: 'internal',
    header: t('routing.aliases.column.internal'),
    render: (a) => <YesNo value={a.internal} />,
  },
  {
    key: 'active',
    header: t('common.status'),
    render: (a) => <ActiveStateBadge state={a.active} />,
  },
];

export function AliasesTab() {
  return (
    <ResourceTab
      permissions={PERMISSIONS.aliases}
      load={api.list}
      remove={api.remove}
      columns={columns}
      Form={AliasForm}
      texts={{
        title: t('routing.aliases.title'),
        description: t('routing.aliases.description'),
        create: t('routing.aliases.new'),
        empty: t('routing.aliases.empty'),
        created: t('routing.created'),
        updated: t('routing.updated'),
        deleted: t('routing.deleted'),
        deleteTitle: t('routing.aliases.delete'),
        deleteConfirm: (a) => t('routing.aliases.deleteConfirm', { address: a.address }),
      }}
    />
  );
}

/** Un alias en estado 2 conserva ese estado mientras siga marcado como activo. */
function nextActive(checked: boolean, current: ActiveState | undefined): ActiveState {
  if (!checked) return 0;
  return current === 2 ? 2 : 1;
}

function AliasForm({ item, onClose, onSaved }: ResourceFormProps<MailAlias>) {
  const [address, setAddress] = useState(item?.address ?? '');
  const [targets, setTargets] = useState<string[]>(item ? gotoToList(item.goto) : []);
  const [senderAllowed, setSenderAllowed] = useState(item?.sender_allowed ?? true);
  const [internal, setInternal] = useState(item?.internal ?? false);
  const [active, setActive] = useState(item ? item.active !== 0 : true);
  const [privateComment, setPrivateComment] = useState(item?.private_comment ?? '');
  const [publicComment, setPublicComment] = useState(item?.public_comment ?? '');
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});

  const action = useAction(async () => {
    const goto = listToGoto(targets);
    if (item) {
      const body = {
        goto: changed(goto, item.goto),
        sender_allowed: changed(senderAllowed, item.sender_allowed),
        internal: changed(internal, item.internal),
        active: changed(nextActive(active, item.active), item.active),
        private_comment: changed(privateComment.trim(), item.private_comment),
        public_comment: changed(publicComment.trim(), item.public_comment),
      };
      if (isEmptyPatch(body)) {
        onClose();
        return;
      }
      await api.update(item.id, body);
    } else {
      await api.create({
        address: normalizeAliasAddress(address) ?? address,
        goto,
        sender_allowed: senderAllowed,
        internal,
        active: nextActive(active, undefined),
        private_comment: privateComment.trim(),
        public_comment: publicComment.trim(),
      });
    }
    onSaved();
  });

  const submit = async () => {
    const next = {
      address: item || normalizeAliasAddress(address) ? undefined : t('validation.aliasAddress'),
      goto: targets.length ? undefined : t('routing.aliases.gotoRequired'),
      private_comment:
        validateField(privateComment, rules.maxLength(COMMENT_MAX_LENGTH)) ?? undefined,
      public_comment:
        validateField(publicComment, rules.maxLength(COMMENT_MAX_LENGTH)) ?? undefined,
    };
    setErrors(next);
    if (Object.values(next).some(Boolean)) return;
    await action.run();
  };

  return (
    <FormModal
      id="alias-form"
      title={item ? t('routing.aliases.editTitle') : t('routing.aliases.createTitle')}
      submitLabel={item ? t('common.save') : t('common.create')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={submit}
      size="lg"
    >
      <FormField
        label={t('routing.aliases.column.address')}
        htmlFor="alias-address"
        required={!item}
        error={errors.address}
        hint={t('routing.aliases.addressHint')}
      >
        <Input
          id="alias-address"
          className="cf-mono"
          value={address}
          onChange={(e) => setAddress(e.target.value)}
          disabled={Boolean(item)}
          invalid={Boolean(errors.address)}
          autoComplete="off"
          spellCheck={false}
        />
      </FormField>
      <FormField
        label={t('routing.aliases.column.goto')}
        htmlFor="alias-goto"
        required
        error={errors.goto}
        hint={t('routing.aliases.gotoHint')}
      >
        <ChipsInput
          id="alias-goto"
          values={targets}
          onChange={setTargets}
          normalize={normalizeEmail}
          invalid={Boolean(errors.goto)}
          placeholder={t('routing.aliases.gotoPlaceholder')}
          removeLabel={(value) => t('common.removeValue', { value })}
          rejectedLabel={(rejected) =>
            t('validation.invalidAddresses', { list: rejected.join(', ') })
          }
        />
      </FormField>
      <Checkbox
        label={t('routing.aliases.senderAllowedLabel')}
        checked={senderAllowed}
        onChange={(e) => setSenderAllowed(e.target.checked)}
      />
      <Checkbox
        label={t('routing.aliases.internalLabel')}
        checked={internal}
        onChange={(e) => setInternal(e.target.checked)}
      />
      <Checkbox
        label={t('common.active')}
        checked={active}
        onChange={(e) => setActive(e.target.checked)}
      />
      <div className="cf-form__row">
        <FormField
          label={t('routing.aliases.publicComment')}
          htmlFor="alias-public"
          error={errors.public_comment}
        >
          <Input
            id="alias-public"
            value={publicComment}
            onChange={(e) => setPublicComment(e.target.value)}
          />
        </FormField>
        <FormField
          label={t('routing.aliases.privateComment')}
          htmlFor="alias-private"
          error={errors.private_comment}
        >
          <Input
            id="alias-private"
            value={privateComment}
            onChange={(e) => setPrivateComment(e.target.value)}
          />
        </FormField>
      </div>
    </FormModal>
  );
}
