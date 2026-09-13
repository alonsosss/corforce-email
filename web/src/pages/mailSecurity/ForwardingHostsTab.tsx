import { useState } from 'react';
import { mailSecurityApi, type ForwardingHost } from '@/api/mailSecurity';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { Badge, Checkbox, FormField, Input, type Column } from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { rules, validateField } from '@/lib/validate';
import { t } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { ListTab, type ResourceFormProps } from '@/pages/shared/ResourceTab';

const loadHosts = () => mailSecurityApi.listForwardingHosts();

const columns: Column<ForwardingHost>[] = [
  {
    key: 'host',
    header: t('security.forwardingHosts.host'),
    render: (h) => <strong className="cf-mono">{h.host}</strong>,
  },
  {
    key: 'source',
    header: t('security.forwardingHosts.source'),
    render: (h) => h.source || t('common.dash'),
  },
  {
    key: 'filter',
    header: t('security.forwardingHosts.filterSpam'),
    render: (h) =>
      h.filter_spam ? (
        <Badge tone="info">{t('security.forwardingHosts.filtered')}</Badge>
      ) : (
        <Badge tone="warning">{t('security.forwardingHosts.unfiltered')}</Badge>
      ),
  },
  { key: 'created', header: t('common.createdAt'), render: (h) => formatDateTime(h.created_at) },
];

export function ForwardingHostsTab() {
  const { can } = useAccess();
  return (
    <ListTab
      load={loadHosts}
      rowKey={(h) => h.id}
      columns={columns}
      Form={ForwardingHostForm}
      canCreate={can(...PERMISSIONS.forwardingHosts.create)}
      canUpdate={false}
      canDelete={can(...PERMISSIONS.forwardingHosts.delete)}
      remove={(h) => mailSecurityApi.deleteForwardingHost(h.id)}
      texts={{
        title: t('security.forwardingHosts.title'),
        description: t('security.forwardingHosts.description'),
        create: t('security.forwardingHosts.new'),
        empty: t('security.forwardingHosts.empty'),
        created: t('security.saved'),
        updated: t('security.saved'),
        deleted: t('security.deleted'),
        deleteTitle: t('security.forwardingHosts.delete'),
        deleteConfirm: (h) => t('security.forwardingHosts.deleteConfirm', { host: h.host }),
      }}
    />
  );
}

function ForwardingHostForm({ onClose, onSaved }: ResourceFormProps<ForwardingHost>) {
  const [host, setHost] = useState('');
  const [source, setSource] = useState('');
  const [filterSpam, setFilterSpam] = useState(true);
  const [hostError, setHostError] = useState<string | null>(null);

  const action = useAction(async () => {
    await mailSecurityApi.createForwardingHost({
      host: host.trim(),
      source: source.trim(),
      filter_spam: filterSpam,
    });
    onSaved();
  });

  const submit = async () => {
    const error = validateField(host, rules.required);
    setHostError(error);
    if (error) return;
    await action.run();
  };

  return (
    <FormModal
      id="forwarding-host-form"
      title={t('security.forwardingHosts.createTitle')}
      submitLabel={t('common.create')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={submit}
    >
      <FormField
        label={t('security.forwardingHosts.host')}
        htmlFor="fwd-host"
        required
        error={hostError}
        hint={t('security.forwardingHosts.hostHint')}
      >
        <Input
          id="fwd-host"
          className="cf-mono"
          value={host}
          onChange={(e) => setHost(e.target.value)}
          invalid={Boolean(hostError)}
          autoComplete="off"
        />
      </FormField>
      <FormField
        label={t('security.forwardingHosts.source')}
        htmlFor="fwd-source"
        hint={t('security.forwardingHosts.sourceHint')}
      >
        <Input id="fwd-source" value={source} onChange={(e) => setSource(e.target.value)} />
      </FormField>
      <Checkbox
        label={t('security.forwardingHosts.filterSpamLabel')}
        checked={filterSpam}
        onChange={(e) => setFilterSpam(e.target.checked)}
      />
    </FormModal>
  );
}
