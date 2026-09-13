import { useState } from 'react';
import { mailRoutingApi, type SenderAcl } from '@/api/mailDirectory';
import { PERMISSIONS } from '@/access/permissions';
import { useAction } from '@/hooks/useAction';
import { Checkbox, FormField, Input, type Column } from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { normalizeAliasAddress, normalizeEmail } from '@/lib/mailAddress';
import { changed, isEmptyPatch } from '@/lib/patch';
import { t } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { ResourceTab, type ResourceFormProps } from '@/pages/shared/ResourceTab';
import { YesNo } from '@/pages/shared/StatusBadges';

const api = mailRoutingApi.senderAcl;
// Comodin de send_as: cualquier remitente. El backend lo exige con external = true.
const ANY_SENDER = '*';

const columns: Column<SenderAcl>[] = [
  {
    key: 'login',
    header: t('routing.senderAcl.column.loggedInAs'),
    render: (a) => <strong className="cf-mono">{a.logged_in_as}</strong>,
  },
  {
    key: 'sendAs',
    header: t('routing.senderAcl.column.sendAs'),
    render: (a) => <span className="cf-mono">{a.send_as}</span>,
  },
  {
    key: 'external',
    header: t('routing.senderAcl.column.external'),
    render: (a) => <YesNo value={a.external} />,
  },
  { key: 'created', header: t('common.createdAt'), render: (a) => formatDateTime(a.created_at) },
];

export function SenderAclTab() {
  return (
    <ResourceTab
      permissions={PERMISSIONS.senderAcl}
      load={api.list}
      remove={api.remove}
      columns={columns}
      Form={SenderAclForm}
      texts={{
        title: t('routing.senderAcl.title'),
        description: t('routing.senderAcl.description'),
        create: t('routing.senderAcl.new'),
        empty: t('routing.senderAcl.empty'),
        created: t('routing.created'),
        updated: t('routing.updated'),
        deleted: t('routing.deleted'),
        deleteTitle: t('routing.senderAcl.delete'),
        deleteConfirm: (a) =>
          t('routing.senderAcl.deleteConfirm', { login: a.logged_in_as, sendAs: a.send_as }),
      }}
    />
  );
}

function normalizeSendAs(raw: string): string | null {
  const value = raw.trim();
  return value === ANY_SENDER ? ANY_SENDER : normalizeAliasAddress(value);
}

function SenderAclForm({ item, onClose, onSaved }: ResourceFormProps<SenderAcl>) {
  const [login, setLogin] = useState(item?.logged_in_as ?? '');
  const [sendAs, setSendAs] = useState(item?.send_as ?? '');
  const [external, setExternal] = useState(item?.external ?? false);
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});

  const action = useAction(async () => {
    const normalized = normalizeSendAs(sendAs) ?? sendAs;
    if (item) {
      const body = {
        send_as: changed(normalized, item.send_as),
        external: changed(external, item.external),
      };
      if (isEmptyPatch(body)) {
        onClose();
        return;
      }
      await api.update(item.id, body);
    } else {
      await api.create({
        logged_in_as: normalizeEmail(login) ?? login,
        send_as: normalized,
        external,
      });
    }
    onSaved();
  });

  const submit = async () => {
    const normalized = normalizeSendAs(sendAs);
    const next = {
      login: item || normalizeEmail(login) ? undefined : t('validation.email'),
      sendAs: normalized
        ? normalized === ANY_SENDER && !external
          ? t('routing.senderAcl.wildcardNeedsExternal')
          : undefined
        : t('routing.senderAcl.sendAsInvalid'),
    };
    setErrors(next);
    if (next.login || next.sendAs) return;
    await action.run();
  };

  return (
    <FormModal
      id="sender-acl-form"
      title={item ? t('routing.senderAcl.editTitle') : t('routing.senderAcl.createTitle')}
      submitLabel={item ? t('common.save') : t('common.create')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={submit}
    >
      <FormField
        label={t('routing.senderAcl.column.loggedInAs')}
        htmlFor="acl-login"
        required={!item}
        error={errors.login}
        hint={t('routing.senderAcl.loggedInAsHint')}
      >
        <Input
          id="acl-login"
          className="cf-mono"
          value={login}
          onChange={(e) => setLogin(e.target.value)}
          disabled={Boolean(item)}
          invalid={Boolean(errors.login)}
          autoComplete="off"
        />
      </FormField>
      <FormField
        label={t('routing.senderAcl.column.sendAs')}
        htmlFor="acl-send-as"
        required
        error={errors.sendAs}
        hint={t('routing.senderAcl.sendAsHint')}
      >
        <Input
          id="acl-send-as"
          className="cf-mono"
          value={sendAs}
          onChange={(e) => setSendAs(e.target.value)}
          invalid={Boolean(errors.sendAs)}
          autoComplete="off"
        />
      </FormField>
      <Checkbox
        label={t('routing.senderAcl.externalLabel')}
        checked={external}
        onChange={(e) => setExternal(e.target.checked)}
      />
    </FormModal>
  );
}
