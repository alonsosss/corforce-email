import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  contactsApi,
  contactsMeta,
  type ConsentApiMethod,
  type ConsentGrantStatus,
  type Consent,
  type Contact,
  type ContactExport,
  type ContactList,
  type ContactsMeta,
} from '@/api/contacts';
import { PICKER_PAGE_SIZE } from '@/api/paging';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import { useResource } from '@/hooks/useResource';
import { useTabParam } from '@/hooks/useTabParam';
import {
  Alert,
  Badge,
  Button,
  Card,
  ConfirmDialog,
  DataTable,
  DescriptionList,
  ErrorState,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  Tabs,
  useToast,
  type Column,
} from '@/design/components';
import { IconDownload, IconEdit, IconMail, IconShieldCheck, IconTrash } from '@/design/icons';
import { saveBlob } from '@/lib/download';
import { formatDateTime, fullName, summarizeUserAgent } from '@/lib/format';
import { rules, validateField } from '@/lib/validate';
import { getLocale, t, tEnum } from '@/i18n';
import { paths } from '@/paths';
import { FormModal } from '@/pages/shared/FormModal';
import { ResourceGate } from '@/pages/shared/ResourceGate';
import { AddToListDialog } from './AddToListDialog';
import { attributeLabel } from './AttributeInput';
import { ContactForm } from './ContactForm';
import { ConsentBadge, ContactStatusBadge, ContactStatusSummary } from './contactStatus';

type TabId = 'data' | 'consents' | 'export';
type Dialog = 'edit' | 'delete' | 'doi' | 'consent' | 'addToList' | null;

export default function ContactDetailPage() {
  const { id = '' } = useParams();
  const navigate = useNavigate();
  const toast = useToast();
  const { can } = useAccess();
  const [dialog, setDialog] = useState<Dialog>(null);
  const contact = useQuery(async () => (await contactsApi.get(id)).data, [id]);

  const canReadAttributes = can(...PERMISSIONS.contactAttributes.read);
  const canReadLists = can(...PERMISSIONS.contactLists.read);
  const definitions = useQuery(
    () => (canReadAttributes ? contactsApi.listAttributes() : Promise.resolve([])),
    [canReadAttributes],
  );
  const lists = useQuery(
    () =>
      canReadLists && can(...PERMISSIONS.contactLists.update)
        ? contactsApi.listLists({ page: 1, per_page: PICKER_PAGE_SIZE })
        : Promise.resolve(null),
    [canReadLists],
  );

  const tabs: { id: TabId; label: string }[] = [
    { id: 'data', label: t('contacts.detail.tab.data') },
    ...(can(...PERMISSIONS.consents.read)
      ? [{ id: 'consents' as const, label: t('contacts.detail.tab.consents') }]
      : []),
    ...(can(...PERMISSIONS.contacts.export)
      ? [{ id: 'export' as const, label: t('contacts.detail.tab.export') }]
      : []),
  ];
  const [tab, setTab] = useTabParam(
    tabs.map((item) => item.id),
    'data',
  );

  if (contact.error) {
    return (
      <div>
        <PageHeader
          title={t('contacts.title')}
          back={{ to: paths.contacts, label: t('nav.contacts') }}
        />
        <Card>
          <ErrorState
            error={contact.error}
            title={t('contacts.notFound')}
            onRetry={contact.reload}
          />
        </Card>
      </div>
    );
  }
  if (!contact.data) {
    return (
      <Card>
        <Skeleton lines={6} />
      </Card>
    );
  }

  const c = contact.data;
  const close = () => setDialog(null);
  const canConsent = can(...PERMISSIONS.consents.create);

  return (
    <div>
      <PageHeader
        title={c.email}
        description={
          <span className="cf-inline">
            <ContactStatusBadge status={c.status} />
            <ConsentBadge status={c.consent_status} />
            {fullName(c.first_name, c.last_name) ? (
              <span>{fullName(c.first_name, c.last_name)}</span>
            ) : null}
          </span>
        }
        back={{ to: paths.contacts, label: t('nav.contacts') }}
        actions={
          <>
            {can(...PERMISSIONS.contacts.update) ? (
              <Button icon={<IconEdit size={16} />} onClick={() => setDialog('edit')}>
                {t('common.edit')}
              </Button>
            ) : null}
            {canConsent ? (
              <Button icon={<IconShieldCheck size={16} />} onClick={() => setDialog('consent')}>
                {t('contacts.consent.record')}
              </Button>
            ) : null}
            {canConsent ? (
              <Button icon={<IconMail size={16} />} onClick={() => setDialog('doi')}>
                {t('contacts.doi.request')}
              </Button>
            ) : null}
            {lists.data ? (
              <Button onClick={() => setDialog('addToList')}>{t('contacts.lists.addOne')}</Button>
            ) : null}
            {can(...PERMISSIONS.contacts.delete) ? (
              <Button
                variant="danger"
                icon={<IconTrash size={16} />}
                onClick={() => setDialog('delete')}
              >
                {t('contacts.delete')}
              </Button>
            ) : null}
          </>
        }
      />
      <Tabs items={tabs} value={tab} onChange={setTab} label={c.email} />
      {tab === 'data' ? <DataTab contact={c} definitions={definitions.data ?? []} /> : null}
      {tab === 'consents' ? <ConsentsTab contactId={c.id} version={c.updated_at} /> : null}
      {tab === 'export' ? <ExportTab contact={c} /> : null}

      {dialog === 'edit' ? (
        <ContactForm
          contact={c}
          onClose={close}
          onSaved={() => {
            toast.success(t('contacts.updated'));
            close();
            contact.reload();
          }}
        />
      ) : null}
      {dialog === 'consent' ? (
        <RecordConsentForm
          contactId={c.id}
          onClose={close}
          onSaved={() => {
            toast.success(t('contacts.consent.recorded'));
            close();
            contact.reload();
          }}
        />
      ) : null}
      {dialog === 'addToList' && lists.data ? (
        <AddToListDialog
          lists={lists.data.items}
          contactIds={[c.id]}
          onClose={close}
          onDone={close}
        />
      ) : null}
      <ConfirmDialog
        open={dialog === 'doi'}
        title={t('contacts.doi.request')}
        message={t('contacts.doi.confirm', { email: c.email })}
        confirmLabel={t('contacts.doi.send')}
        onCancel={close}
        onConfirm={async () => {
          const { data } = await contactsApi.requestConfirmation(c.id);
          toast.success(t('contacts.doi.sent', { date: formatDateTime(data.expires_at) }));
          close();
          contact.reload();
        }}
      />
      <ConfirmDialog
        open={dialog === 'delete'}
        title={t('contacts.delete')}
        message={t('contacts.deleteConfirm', { email: c.email })}
        confirmLabel={t('contacts.delete')}
        danger
        onCancel={close}
        onConfirm={async () => {
          await contactsApi.remove(c.id);
          toast.success(t('contacts.deleted'));
          navigate(paths.contacts, { replace: true });
        }}
      />
    </div>
  );
}

function DataTab({
  contact,
  definitions,
}: {
  contact: Contact;
  definitions: readonly { key: string; label: string; type: string }[];
}) {
  const attributes = contact.attributes ?? {};
  const keys = Object.keys(attributes).sort();
  const labelOf = (key: string) => {
    const def = definitions.find((d) => d.key === key);
    return def ? attributeLabel(def as Parameters<typeof attributeLabel>[0]) : key;
  };
  const tags = contact.tags ?? [];
  return (
    <div className="cf-stack">
      <Card title={t('contacts.detail.data')}>
        <DescriptionList
          items={[
            { label: t('common.email'), value: <span className="cf-mono">{contact.email}</span> },
            { label: t('contacts.form.firstName'), value: contact.first_name || t('common.dash') },
            { label: t('contacts.form.lastName'), value: contact.last_name || t('common.dash') },
            { label: t('common.status'), value: <ContactStatusSummary status={contact.status} /> },
            {
              label: t('contacts.column.consent'),
              value: <ConsentBadge status={contact.consent_status} />,
            },
            { label: t('contacts.column.source'), value: tEnum('contacts.source', contact.source) },
            { label: t('contacts.form.locale'), value: contact.locale ?? t('common.dash') },
            { label: t('contacts.form.timezone'), value: contact.timezone ?? t('common.dash') },
            {
              label: t('contacts.column.tags'),
              value: tags.length ? (
                <span className="cf-inline-list">
                  {tags.map((tag) => (
                    <Badge key={tag}>{tag}</Badge>
                  ))}
                </span>
              ) : (
                t('common.dash')
              ),
            },
            { label: t('common.createdAt'), value: formatDateTime(contact.created_at) },
            { label: t('common.updatedAt'), value: formatDateTime(contact.updated_at) },
          ]}
        />
      </Card>
      <Card title={t('contacts.attributes.title')}>
        {keys.length ? (
          <DescriptionList
            items={keys.map((key) => ({
              label: labelOf(key),
              value: <span className="cf-break">{String(attributes[key])}</span>,
            }))}
          />
        ) : (
          <span className="cf-text-muted cf-text-sm">{t('contacts.attributes.noneOnContact')}</span>
        )}
      </Card>
    </div>
  );
}

function ConsentsTab({ contactId, version }: { contactId: string; version: string }) {
  const consents = useQuery(() => contactsApi.listConsents(contactId), [contactId, version]);
  const columns: Column<Consent>[] = [
    {
      key: 'when',
      header: t('contacts.consent.when'),
      render: (c) => formatDateTime(c.occurred_at),
    },
    {
      key: 'status',
      header: t('common.status'),
      render: (c) => <ConsentBadge status={c.status} />,
    },
    {
      key: 'method',
      header: t('contacts.consent.method'),
      render: (c) => tEnum('contacts.consentMethod', c.method),
    },
    {
      key: 'source',
      header: t('contacts.consent.source'),
      render: (c) => <span className="cf-break">{c.source || t('common.dash')}</span>,
    },
    {
      key: 'ip',
      header: t('contacts.consent.ip'),
      render: (c) => <span className="cf-mono">{c.ip ?? t('common.dash')}</span>,
    },
    {
      key: 'ua',
      header: t('contacts.consent.userAgent'),
      render: (c) =>
        c.user_agent ? (
          <span title={c.user_agent}>{summarizeUserAgent(c.user_agent)}</span>
        ) : (
          t('common.dash')
        ),
    },
    {
      key: 'evidence',
      header: t('contacts.consent.evidence'),
      render: (c) =>
        c.evidence && Object.keys(c.evidence).length ? (
          <details>
            <summary>{t('contacts.consent.showEvidence')}</summary>
            <pre className="cf-pre">{JSON.stringify(c.evidence, null, 2)}</pre>
          </details>
        ) : (
          t('common.dash')
        ),
    },
  ];
  return (
    <Card flush title={t('contacts.consent.title')} description={t('contacts.consent.appendOnly')}>
      <DataTable
        columns={columns}
        rows={consents.data ?? []}
        rowKey={(c) => c.id}
        loading={consents.loading}
        error={consents.error}
        onRetry={consents.reload}
        empty={{ title: t('contacts.consent.empty') }}
      />
    </Card>
  );
}

function downloadJson(filename: string, data: unknown) {
  saveBlob(new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' }), filename);
}

function ExportTab({ contact }: { contact: Contact }) {
  const toast = useToast();
  const { can } = useAccess();
  const [data, setData] = useState<ContactExport | null>(null);
  const format = new Intl.NumberFormat(getLocale());
  const run = useAction(async () => {
    setData((await contactsApi.exportData(contact.id)).data);
  });
  const remove = useAction(async (list: ContactList) => {
    const { data: result } = await contactsApi.removeMembers(list.id, [contact.id]);
    toast.success(
      t('contacts.lists.removedResult', { removed: result.removed, ignored: result.ignored }),
    );
    await run.run();
  });
  const canUpdateLists = can(...PERMISSIONS.contactLists.update);

  const columns: Column<ContactList>[] = [
    { key: 'name', header: t('common.name'), render: (l) => <strong>{l.name}</strong> },
    {
      key: 'members',
      header: t('contacts.lists.members'),
      align: 'right',
      render: (l) => format.format(l.member_count),
    },
    ...(canUpdateLists
      ? [
          {
            key: 'actions',
            header: '',
            align: 'right' as const,
            render: (l: ContactList) => (
              <Button
                size="sm"
                variant="ghost"
                loading={remove.busy}
                onClick={() => void remove.run(l)}
              >
                {t('contacts.lists.removeOne')}
              </Button>
            ),
          },
        ]
      : []),
  ];

  return (
    <div className="cf-stack">
      <Card
        title={t('contacts.export.title')}
        description={t('contacts.export.description')}
        actions={
          <Button variant="primary" loading={run.busy} onClick={() => void run.run()}>
            {data ? t('contacts.export.refresh') : t('contacts.export.run')}
          </Button>
        }
      >
        {run.error ? <ErrorState error={run.error} onRetry={() => void run.run()} /> : null}
        {remove.error ? <ErrorState error={remove.error} /> : null}
        {data ? (
          <div className="cf-stack" style={{ gap: 'var(--cf-space-3)' }}>
            <DescriptionList
              items={[
                { label: t('contacts.export.exportedAt'), value: formatDateTime(data.exported_at) },
                { label: t('contacts.consent.title'), value: format.format(data.consents.length) },
                { label: t('contacts.tab.lists'), value: format.format(data.lists.length) },
              ]}
            />
            <div className="cf-inline">
              <Button
                icon={<IconDownload size={16} />}
                onClick={() => downloadJson(`contacto-${contact.id}.json`, data)}
              >
                {t('contacts.export.download')}
              </Button>
              <Button variant="ghost" onClick={() => setData(null)}>
                {t('contacts.export.discard')}
              </Button>
            </div>
          </div>
        ) : !run.error ? (
          <Alert tone="info">{t('contacts.export.hint')}</Alert>
        ) : null}
      </Card>
      {data ? (
        <Card flush title={t('contacts.export.lists')}>
          <DataTable
            columns={columns}
            rows={data.lists}
            rowKey={(l) => l.id}
            empty={{ title: t('contacts.export.noLists') }}
          />
        </Card>
      ) : null}
    </div>
  );
}

interface RecordConsentProps {
  contactId: string;
  onClose: () => void;
  onSaved: () => void;
}

function RecordConsentForm(props: RecordConsentProps) {
  const meta = useResource(contactsMeta);
  return (
    <ResourceGate
      resource={meta}
      modal={{ title: t('contacts.consent.record'), onClose: props.onClose }}
    >
      {(catalog) => <RecordConsentBody {...props} meta={catalog} />}
    </ResourceGate>
  );
}

function RecordConsentBody({
  contactId,
  onClose,
  onSaved,
  meta,
}: RecordConsentProps & { meta: ContactsMeta }) {
  const [status, setStatus] = useState<ConsentGrantStatus | ''>(meta.api_consent_statuses[0] ?? '');
  const [method, setMethod] = useState<ConsentApiMethod | ''>(meta.api_consent_methods[0] ?? '');
  const [source, setSource] = useState('');
  const [ip, setIp] = useState('');
  const [userAgent, setUserAgent] = useState('');
  const [error, setError] = useState<string | null>(null);

  const action = useAction(
    async (chosenStatus: ConsentGrantStatus, chosenMethod: ConsentApiMethod) => {
      await contactsApi.recordConsent(contactId, {
        status: chosenStatus,
        method: chosenMethod,
        source: source.trim(),
        ip: ip.trim() || undefined,
        user_agent: userAgent.trim() || undefined,
      });
      onSaved();
    },
  );

  return (
    <FormModal
      id="contact-consent-form"
      title={t('contacts.consent.record')}
      submitLabel={t('contacts.consent.save')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={async () => {
        const next = validateField(source, rules.required);
        setError(next);
        if (!next && status && method) await action.run(status, method);
      }}
    >
      <Alert tone="info">{t('contacts.consent.recordHint')}</Alert>
      <div className="cf-form__row">
        <FormField label={t('common.status')} htmlFor="consent-status">
          <Select
            id="consent-status"
            options={meta.api_consent_statuses.map((s) => ({
              value: s,
              label: tEnum('contacts.consentAction', s),
            }))}
            value={status}
            onChange={(e) => setStatus(e.target.value as ConsentGrantStatus)}
          />
        </FormField>
        <FormField label={t('contacts.consent.method')} htmlFor="consent-method">
          <Select
            id="consent-method"
            options={meta.api_consent_methods.map((m) => ({
              value: m,
              label: tEnum('contacts.consentMethod', m),
            }))}
            value={method}
            onChange={(e) => setMethod(e.target.value as ConsentApiMethod)}
          />
        </FormField>
      </div>
      <FormField
        label={t('contacts.consent.source')}
        htmlFor="consent-source"
        required
        error={error}
        hint={t('contacts.consent.sourceHint')}
      >
        <Input
          id="consent-source"
          value={source}
          onChange={(e) => setSource(e.target.value)}
          invalid={Boolean(error)}
        />
      </FormField>
      <div className="cf-form__row">
        <FormField
          label={t('contacts.consent.ip')}
          htmlFor="consent-ip"
          hint={t('contacts.consent.ipHint')}
        >
          <Input
            id="consent-ip"
            className="cf-mono"
            value={ip}
            onChange={(e) => setIp(e.target.value)}
          />
        </FormField>
        <FormField label={t('contacts.consent.userAgent')} htmlFor="consent-ua">
          <Input id="consent-ua" value={userAgent} onChange={(e) => setUserAgent(e.target.value)} />
        </FormField>
      </div>
    </FormModal>
  );
}
