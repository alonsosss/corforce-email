import { useState } from 'react';
import { mailSecurityApi, type MailboxTags } from '@/api/mailSecurity';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { Checkbox, FormField, Input, type Column } from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { normalizeEmail } from '@/lib/mailAddress';
import { t } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { ListTab, type ResourceFormProps } from '@/pages/shared/ResourceTab';
import { YesNo } from '@/pages/shared/StatusBadges';

const loadTags = () => mailSecurityApi.listMailboxTags();

const columns: Column<MailboxTags>[] = [
  {
    key: 'username',
    header: t('security.mailboxTags.mailbox'),
    render: (m) => <strong className="cf-mono">{m.username}</strong>,
  },
  {
    key: 'subject',
    header: t('security.mailboxTags.subjectTag'),
    render: (m) => <YesNo value={m.subject_tag} />,
  },
  {
    key: 'folder',
    header: t('security.mailboxTags.subfolderTag'),
    render: (m) => <YesNo value={m.subfolder_tag} />,
  },
  { key: 'updated', header: t('common.updatedAt'), render: (m) => formatDateTime(m.updated_at) },
];

export function MailboxTagsTab() {
  const { can } = useAccess();
  const canUpdate = can(...PERMISSIONS.mailboxTags.update);
  return (
    <ListTab
      load={loadTags}
      rowKey={(m) => m.username}
      columns={columns}
      Form={MailboxTagsForm}
      canCreate={canUpdate}
      canUpdate={canUpdate}
      canDelete={false}
      remove={() => Promise.resolve()}
      texts={{
        title: t('security.mailboxTags.title'),
        description: t('security.mailboxTags.description'),
        create: t('security.mailboxTags.new'),
        empty: t('security.mailboxTags.empty'),
        created: t('security.saved'),
        updated: t('security.saved'),
        deleted: t('security.deleted'),
        deleteTitle: t('common.delete'),
        deleteConfirm: (m) => m.username,
      }}
    />
  );
}

function MailboxTagsForm({ item, onClose, onSaved }: ResourceFormProps<MailboxTags>) {
  const [username, setUsername] = useState(item?.username ?? '');
  const [subjectTag, setSubjectTag] = useState(item?.subject_tag ?? false);
  const [subfolderTag, setSubfolderTag] = useState(item?.subfolder_tag ?? false);
  const [usernameError, setUsernameError] = useState<string | null>(null);

  const action = useAction(async (target: string) => {
    await mailSecurityApi.putMailboxTags(target, {
      subject_tag: subjectTag,
      subfolder_tag: subfolderTag,
    });
    onSaved();
  });

  const submit = async () => {
    const target = item ? item.username : normalizeEmail(username);
    setUsernameError(target ? null : t('validation.email'));
    if (!target) return;
    await action.run(target);
  };

  return (
    <FormModal
      id="mailbox-tags-form"
      title={item ? t('security.mailboxTags.editTitle') : t('security.mailboxTags.createTitle')}
      submitLabel={t('common.save')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={submit}
    >
      <FormField
        label={t('security.mailboxTags.mailbox')}
        htmlFor="tags-username"
        required={!item}
        error={usernameError}
      >
        <Input
          id="tags-username"
          className="cf-mono"
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          disabled={Boolean(item)}
          invalid={Boolean(usernameError)}
          autoComplete="off"
        />
      </FormField>
      <span className="cf-text-sm cf-text-secondary">{t('security.mailboxTags.explain')}</span>
      <Checkbox
        label={t('security.mailboxTags.subjectTagLabel')}
        checked={subjectTag}
        onChange={(e) => setSubjectTag(e.target.checked)}
      />
      <Checkbox
        label={t('security.mailboxTags.subfolderTagLabel')}
        checked={subfolderTag}
        onChange={(e) => setSubfolderTag(e.target.checked)}
      />
    </FormModal>
  );
}
