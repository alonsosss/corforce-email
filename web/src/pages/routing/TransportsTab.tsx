import { useState } from 'react';
import { isPlatformTransport, mailRoutingApi, type Transport } from '@/api/mailDirectory';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { Badge, Checkbox, FormField, Input, type Column } from '@/design/components';
import { changed, isEmptyPatch } from '@/lib/patch';
import { rules, validateField } from '@/lib/validate';
import { t } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { ResourceTab, type ResourceFormProps } from '@/pages/shared/ResourceTab';
import { ActiveBadge, YesNo } from '@/pages/shared/StatusBadges';
import { PasswordStatus, WriteOnlyPassword, passwordPatch } from './credentials';

const api = mailRoutingApi.transports;
const CREDENTIAL_MAX_LENGTH = 255;

const columns: Column<Transport>[] = [
  {
    key: 'destination',
    header: t('routing.transports.column.destination'),
    render: (r) => <strong className="cf-mono">{r.destination}</strong>,
  },
  {
    key: 'nexthop',
    header: t('routing.transports.column.nexthop'),
    render: (r) => <span className="cf-mono">{r.nexthop}</span>,
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
  {
    key: 'mx',
    header: t('routing.transports.column.mxBased'),
    render: (r) => <YesNo value={r.is_mx_based} />,
  },
  {
    key: 'scope',
    header: t('routing.transports.column.scope'),
    render: (r) =>
      isPlatformTransport(r) ? (
        <Badge tone="accent">{t('routing.transports.platform')}</Badge>
      ) : (
        <Badge>{t('routing.transports.tenant')}</Badge>
      ),
  },
  { key: 'active', header: t('common.status'), render: (r) => <ActiveBadge active={r.active} /> },
];

export function TransportsTab() {
  const { isSuperadmin } = useAccess();
  return (
    <ResourceTab
      permissions={PERMISSIONS.transports}
      load={api.list}
      remove={api.remove}
      columns={columns}
      Form={TransportForm}
      // Las rutas de plataforma las ven todas las empresas; solo el operador las cambia.
      isLocked={(r) => isPlatformTransport(r) && !isSuperadmin}
      texts={{
        title: t('routing.transports.title'),
        description: t('routing.transports.description'),
        create: t('routing.transports.new'),
        empty: t('routing.transports.empty'),
        created: t('routing.created'),
        updated: t('routing.updated'),
        deleted: t('routing.deleted'),
        deleteTitle: t('routing.transports.delete'),
        deleteConfirm: (r) => t('routing.transports.deleteConfirm', { destination: r.destination }),
      }}
    />
  );
}

function TransportForm({ item, onClose, onSaved }: ResourceFormProps<Transport>) {
  const { isSuperadmin } = useAccess();
  const [destination, setDestination] = useState(item?.destination ?? '');
  const [nexthop, setNexthop] = useState(item?.nexthop ?? '');
  const [username, setUsername] = useState(item?.username ?? '');
  const [password, setPassword] = useState('');
  const [clearPassword, setClearPassword] = useState(false);
  const [mxBased, setMxBased] = useState(item?.is_mx_based ?? false);
  const [active, setActive] = useState(item?.active ?? true);
  const [platform, setPlatform] = useState(false);
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});

  const action = useAction(async () => {
    if (item) {
      const body = {
        destination: changed(destination.trim(), item.destination),
        nexthop: changed(nexthop.trim(), item.nexthop),
        username: changed(username.trim(), item.username),
        password: passwordPatch(password, clearPassword),
        is_mx_based: changed(mxBased, item.is_mx_based),
        active: changed(active, item.active),
      };
      if (isEmptyPatch(body)) {
        onClose();
        return;
      }
      await api.update(item.id, body);
    } else {
      await api.create({
        destination: destination.trim(),
        nexthop: nexthop.trim(),
        username: username.trim(),
        password,
        is_mx_based: mxBased,
        active,
        platform: isSuperadmin && platform,
      });
    }
    onSaved();
  });

  const submit = async () => {
    const next = {
      destination: validateField(destination, rules.required) ?? undefined,
      nexthop: validateField(nexthop, rules.required) ?? undefined,
      username: validateField(username, rules.maxLength(CREDENTIAL_MAX_LENGTH)) ?? undefined,
      password: validateField(password, rules.maxLength(CREDENTIAL_MAX_LENGTH)) ?? undefined,
    };
    setErrors(next);
    if (Object.values(next).some(Boolean)) return;
    await action.run();
  };

  return (
    <FormModal
      id="transport-form"
      title={item ? t('routing.transports.editTitle') : t('routing.transports.createTitle')}
      submitLabel={item ? t('common.save') : t('common.create')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={submit}
    >
      <div className="cf-form__row">
        <FormField
          label={t('routing.transports.column.destination')}
          htmlFor="transport-destination"
          required
          error={errors.destination}
          hint={t('routing.transports.destinationHint')}
        >
          <Input
            id="transport-destination"
            className="cf-mono"
            value={destination}
            onChange={(e) => setDestination(e.target.value)}
            invalid={Boolean(errors.destination)}
            autoComplete="off"
          />
        </FormField>
        <FormField
          label={t('routing.transports.column.nexthop')}
          htmlFor="transport-nexthop"
          required
          error={errors.nexthop}
          hint={t('routing.relayhosts.hostnameHint')}
        >
          <Input
            id="transport-nexthop"
            className="cf-mono"
            value={nexthop}
            onChange={(e) => setNexthop(e.target.value)}
            invalid={Boolean(errors.nexthop)}
            autoComplete="off"
          />
        </FormField>
      </div>
      <FormField
        label={t('routing.column.username')}
        htmlFor="transport-user"
        error={errors.username}
      >
        <Input
          id="transport-user"
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          autoComplete="off"
        />
      </FormField>
      <WriteOnlyPassword
        id="transport-password"
        configured={item ? item.has_password : null}
        value={password}
        onChange={setPassword}
        clear={clearPassword}
        onClearChange={setClearPassword}
      />
      <Checkbox
        label={t('routing.transports.mxBasedLabel')}
        checked={mxBased}
        onChange={(e) => setMxBased(e.target.checked)}
      />
      <Checkbox
        label={t('common.active')}
        checked={active}
        onChange={(e) => setActive(e.target.checked)}
      />
      {!item && isSuperadmin ? (
        <Checkbox
          label={t('routing.transports.platformLabel')}
          checked={platform}
          onChange={(e) => setPlatform(e.target.checked)}
        />
      ) : null}
    </FormModal>
  );
}
