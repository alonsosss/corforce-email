import { useState } from 'react';
import { mailSecurityApi, type DomainFooter } from '@/api/mailSecurity';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import {
  Badge,
  Checkbox,
  ChipsInput,
  FormField,
  HtmlPreviewFrame,
  Textarea,
  type Column,
} from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { normalizeDomainName, normalizeEmail } from '@/lib/mailAddress';
import { t } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { ListTab, type ResourceFormProps } from '@/pages/shared/ResourceTab';
import { DirectoryDomainPicker } from '@/pages/shared/DirectoryDomainPicker';
import { YesNo } from '@/pages/shared/StatusBadges';

const loadFooters = () => mailSecurityApi.listFooters();

const columns: Column<DomainFooter>[] = [
  {
    key: 'domain',
    header: t('domains.column.domain'),
    render: (f) => <strong className="cf-mono">{f.domain}</strong>,
  },
  {
    key: 'content',
    header: t('security.footers.content'),
    render: (f) => (
      <span className="cf-inline-list">
        {f.html.trim() ? <Badge tone="info">{t('security.footers.html')}</Badge> : null}
        {f.plain.trim() ? <Badge>{t('security.footers.plain')}</Badge> : null}
      </span>
    ),
  },
  {
    key: 'exclusions',
    header: t('security.footers.exclusions'),
    align: 'right',
    render: (f) => (f.mailbox_exclude?.length ?? 0) + (f.alias_domain_exclude?.length ?? 0),
  },
  {
    key: 'replies',
    header: t('security.footers.skipReplies'),
    render: (f) => <YesNo value={f.skip_replies} />,
  },
  { key: 'updated', header: t('common.updatedAt'), render: (f) => formatDateTime(f.updated_at) },
];

export function FootersTab() {
  const { can } = useAccess();
  const canUpdate = can(...PERMISSIONS.footers.update);
  return (
    <ListTab
      load={loadFooters}
      rowKey={(f) => f.domain}
      columns={columns}
      Form={FooterForm}
      canCreate={canUpdate}
      canUpdate={canUpdate}
      canDelete={can(...PERMISSIONS.footers.delete)}
      remove={(f) => mailSecurityApi.deleteFooter(f.domain)}
      texts={{
        title: t('security.footers.title'),
        description: t('security.footers.description'),
        create: t('security.footers.new'),
        empty: t('security.footers.empty'),
        created: t('security.saved'),
        updated: t('security.saved'),
        deleted: t('security.deleted'),
        deleteTitle: t('security.footers.delete'),
        deleteConfirm: (f) => t('security.footers.deleteConfirm', { domain: f.domain }),
      }}
    />
  );
}

function FooterForm({ item, onClose, onSaved }: ResourceFormProps<DomainFooter>) {
  const [domain, setDomain] = useState(item?.domain ?? '');
  const [html, setHtml] = useState(item?.html ?? '');
  const [plain, setPlain] = useState(item?.plain ?? '');
  const [mailboxExclude, setMailboxExclude] = useState<string[]>(item?.mailbox_exclude ?? []);
  const [aliasExclude, setAliasExclude] = useState<string[]>(item?.alias_domain_exclude ?? []);
  const [skipReplies, setSkipReplies] = useState(item?.skip_replies ?? false);
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});

  const action = useAction(async (target: string) => {
    await mailSecurityApi.putFooter(target, {
      html,
      plain,
      mailbox_exclude: mailboxExclude,
      alias_domain_exclude: aliasExclude,
      skip_replies: skipReplies,
    });
    onSaved();
  });

  const submit = async () => {
    const target = item ? item.domain : normalizeDomainName(domain);
    const next = {
      domain: target ? undefined : t('validation.domain'),
      content: html.trim() || plain.trim() ? undefined : t('security.footers.contentRequired'),
    };
    setErrors(next);
    if (!target || next.content) return;
    await action.run(target);
  };

  const chipLabels = {
    removeLabel: (value: string) => t('common.removeValue', { value }),
    rejectedLabel: (rejected: string[]) =>
      t('validation.invalidValues', { list: rejected.join(', ') }),
  };

  return (
    <FormModal
      id="footer-form"
      title={
        item
          ? t('security.footers.editTitle', { domain: item.domain })
          : t('security.footers.createTitle')
      }
      submitLabel={t('common.save')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={submit}
      size="lg"
    >
      {item ? null : (
        <FormField
          label={t('domains.column.domain')}
          htmlFor="footer-domain"
          required
          error={errors.domain}
        >
          <DirectoryDomainPicker
            id="footer-domain"
            placeholder={t('common.select')}
            value={domain}
            onChange={setDomain}
            invalid={Boolean(errors.domain)}
          />
        </FormField>
      )}
      <div className="cf-split">
        <FormField
          label={t('security.footers.html')}
          htmlFor="footer-html"
          hint={t('security.footers.htmlHint')}
        >
          <Textarea
            id="footer-html"
            mono
            rows={12}
            value={html}
            onChange={(e) => setHtml(e.target.value)}
          />
        </FormField>
        <div className="cf-field">
          <span className="cf-field__label">{t('common.preview')}</span>
          <HtmlPreviewFrame html={html} title={t('security.footers.previewTitle')} height={240} />
        </div>
      </div>
      <FormField label={t('security.footers.plain')} htmlFor="footer-plain" error={errors.content}>
        <Textarea
          id="footer-plain"
          rows={5}
          value={plain}
          onChange={(e) => setPlain(e.target.value)}
        />
      </FormField>
      <FormField
        label={t('security.footers.mailboxExclude')}
        htmlFor="footer-mailbox-exclude"
        hint={t('security.footers.mailboxExcludeHint')}
      >
        <ChipsInput
          id="footer-mailbox-exclude"
          values={mailboxExclude}
          onChange={setMailboxExclude}
          normalize={normalizeEmail}
          {...chipLabels}
        />
      </FormField>
      <FormField
        label={t('security.footers.aliasDomainExclude')}
        htmlFor="footer-alias-exclude"
        hint={t('security.footers.aliasDomainExcludeHint')}
      >
        <ChipsInput
          id="footer-alias-exclude"
          values={aliasExclude}
          onChange={setAliasExclude}
          normalize={normalizeDomainName}
          {...chipLabels}
        />
      </FormField>
      <Checkbox
        label={t('security.footers.skipRepliesLabel')}
        checked={skipReplies}
        onChange={(e) => setSkipReplies(e.target.checked)}
      />
    </FormModal>
  );
}
