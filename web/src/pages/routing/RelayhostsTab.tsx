import { useState } from 'react';
import { mailRoutingApi, type Relayhost } from '@/api/mailDirectory';
import { PERMISSIONS } from '@/access/permissions';
import { useAction } from '@/hooks/useAction';
import { Checkbox, FormField, Input, type Column } from '@/design/components';
import { changed, isEmptyPatch } from '@/lib/patch';
import { rules, validateField } from '@/lib/validate';
import { t } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { ResourceTab, type ResourceFormProps } from '@/pages/shared/ResourceTab';
import { ActiveBadge } from '@/pages/shared/StatusBadges';
import { PasswordStatus, WriteOnlyPassword, passwordPatch } from './credentials';

const api = mailRoutingApi.relayhosts;
const CREDENTIAL_MAX_LENGTH = 255;

const columns: Column<Relayhost>[] = [
  {
    key: 'hostname',
    header: t('routing.relayhosts.column.hostname'),
    render: (r) => <strong className="cf-mono">{r.hostname}</strong>,
  },
  {
    key: 'username',
    header: t('routing.column.username'),
    render: (r) => r.username || t('common.dash'),
  },
  {
    key: 'password',
    header: t('common.password'),
    render: (r) => <PasswordStatus configured={r.has_password} />,
  },
  { key: 'active', header: t('common.status'), render: (r) => <ActiveBadge active={r.active} /> },
];

export function RelayhostsTab() {
  return (
    <ResourceTab
      permissions={PERMISSIONS.relayhosts}
      load={api.list}
      remove={api.remove}
      columns={columns}
      Form={RelayhostForm}
      texts={{
        title: t('routing.relayhosts.title'),
        description: t('routing.relayhosts.description'),
        create: t('routing.relayhosts.new'),
        empty: t('routing.relayhosts.empty'),
        created: t('routing.created'),
        updated: t('routing.updated'),
        deleted: t('routing.deleted'),
        deleteTitle: t('routing.relayhosts.delete'),
        deleteConfirm: (r) => t('routing.relayhosts.deleteConfirm', { host: r.hostname }),
      }}
    />
  );
}

function RelayhostForm({ item, onClose, onSaved }: ResourceFormProps<Relayhost>) {
  const [hostname, setHostname] = useState(item?.hostname ?? '');
  const [username, setUsername] = useState(item?.username ?? '');
  const [password, setPassword] = useState('');
  const [clearPassword, setClearPassword] = useState(false);
  const [active, setActive] = useState(item?.active ?? true);
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});

  const action = useAction(async () => {
    if (item) {
      const body = {
        hostname: changed(hostname.trim(), item.hostname),
        username: changed(username.trim(), item.username),
        password: passwordPatch(password, clearPassword),
        active: changed(active, item.active),
      };
      if (isEmptyPatch(body)) {
        onClose();
        return;
      }
      await api.update(item.id, body);
    } else {
      await api.create({ hostname: hostname.trim(), username: username.trim(), password, active });
    }
    onSaved();
  });

  const submit = async () => {
    const next = {
      hostname: validateField(hostname, rules.required) ?? undefined,
      username: validateField(username, rules.maxLength(CREDENTIAL_MAX_LENGTH)) ?? undefined,
      password: validateField(password, rules.maxLength(CREDENTIAL_MAX_LENGTH)) ?? undefined,
    };
    setErrors(next);
    if (Object.values(next).some(Boolean)) return;
    await action.run();
  };

  return (
    <FormModal
      id="relayhost-form"
      title={item ? t('routing.relayhosts.editTitle') : t('routing.relayhosts.createTitle')}
      submitLabel={item ? t('common.save') : t('common.create')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={submit}
    >
      <FormField
        label={t('routing.relayhosts.column.hostname')}
        htmlFor="relayhost-host"
        required
        error={errors.hostname}
        hint={t('routing.relayhosts.hostnameHint')}
      >
        <Input
          id="relayhost-host"
          className="cf-mono"
          value={hostname}
          onChange={(e) => setHostname(e.target.value)}
          invalid={Boolean(errors.hostname)}
          autoComplete="off"
        />
      </FormField>
      <FormField
        label={t('routing.column.username')}
        htmlFor="relayhost-user"
        error={errors.username}
      >
        <Input
          id="relayhost-user"
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          autoComplete="off"
        />
      </FormField>
      <WriteOnlyPassword
        id="relayhost-password"
        configured={item ? item.has_password : null}
        value={password}
        onChange={setPassword}
        clear={clearPassword}
        onClearChange={setClearPassword}
      />
      <Checkbox
        label={t('common.active')}
        checked={active}
        onChange={(e) => setActive(e.target.checked)}
      />
    </FormModal>
  );
}
