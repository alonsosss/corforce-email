import { useMemo, useState, type ChangeEvent, type FormEvent } from 'react';
import { contactsApi, type ContactImport, type ImportResult } from '@/api/contacts';
import { PICKER_PAGE_SIZE } from '@/api/paging';
import { errorMessage } from '@/api/messages';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import {
  Alert,
  Badge,
  Button,
  Card,
  Checkbox,
  DataTable,
  FormField,
  Modal,
  Select,
  Textarea,
  useToast,
  type Column,
} from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { getLocale, t, tEnum } from '@/i18n';
import { rowsFromCsv, TAG_SEPARATOR } from './importRows';

type ConsentChoice = 'none' | 'granted';

export function ImportTab() {
  const toast = useToast();
  const { can } = useAccess();
  const pager = usePagination();
  const format = new Intl.NumberFormat(getLocale());
  const canReadAttributes = can(...PERMISSIONS.contactAttributes.read);
  const canReadLists = can(...PERMISSIONS.contactLists.read);

  const [text, setText] = useState('');
  const [consent, setConsent] = useState<ConsentChoice>('none');
  const [basis, setBasis] = useState('');
  const [listId, setListId] = useState('');
  const [updateExisting, setUpdateExisting] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [basisError, setBasisError] = useState<string | null>(null);
  const [result, setResult] = useState<ImportResult | null>(null);
  const [viewing, setViewing] = useState<ContactImport | null>(null);

  const definitions = useQuery(
    () => (canReadAttributes ? contactsApi.listAttributes() : Promise.resolve([])),
    [canReadAttributes],
  );
  const lists = useQuery(
    () =>
      canReadLists
        ? contactsApi.listLists({ page: 1, per_page: PICKER_PAGE_SIZE })
        : Promise.resolve(null),
    [canReadLists],
  );
  const imports = useQuery(
    () => contactsApi.listImports({ page: pager.page, per_page: pager.perPage }),
    [pager.page, pager.perPage],
  );
  const parsed = useMemo(
    () => (text.trim() ? rowsFromCsv(text, definitions.data ?? []) : null),
    [text, definitions.data],
  );

  const action = useAction(async () => {
    if (!parsed) return;
    const { data } = await contactsApi.importContacts({
      rows: parsed.rows,
      list_id: listId || undefined,
      update_existing: updateExisting,
      consent:
        consent === 'granted' ? { status: 'granted', basis: basis.trim() } : { status: 'none' },
    });
    setResult(data);
    setText('');
    toast.success(t('contacts.import.done'));
    imports.reload();
  });

  const onFile = async (e: ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (file) setText(await file.text());
    e.target.value = '';
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const problem = !parsed
      ? t('contacts.import.empty')
      : parsed.missingEmail
        ? t('contacts.import.missingEmail')
        : parsed.unknownColumns.length
          ? t('contacts.import.unknownColumns', { list: parsed.unknownColumns.join(', ') })
          : parsed.rows.length === 0
            ? t('contacts.import.noRows')
            : null;
    const needsBasis = consent === 'granted' && !basis.trim();
    setFormError(problem);
    setBasisError(needsBasis ? t('validation.required') : null);
    if (problem || needsBasis) return;
    setResult(null);
    await action.run();
  };

  const historyColumns: Column<ContactImport>[] = [
    { key: 'when', header: t('common.createdAt'), render: (i) => formatDateTime(i.created_at) },
    {
      key: 'status',
      header: t('common.status'),
      render: (i) => (
        <Badge tone={i.status === 'completed' ? 'success' : 'danger'}>
          {tEnum('contacts.import.status', i.status)}
        </Badge>
      ),
    },
    {
      key: 'total',
      header: t('contacts.import.total'),
      align: 'right',
      render: (i) => format.format(i.total),
    },
    {
      key: 'created',
      header: t('contacts.import.created'),
      align: 'right',
      render: (i) => format.format(i.created),
    },
    {
      key: 'updated',
      header: t('contacts.import.updated'),
      align: 'right',
      render: (i) => format.format(i.updated),
    },
    {
      key: 'skipped',
      header: t('contacts.import.skipped'),
      align: 'right',
      render: (i) => format.format(i.skipped),
    },
    {
      key: 'consent',
      header: t('contacts.import.consentColumn'),
      render: (i) =>
        i.consent_basis ? t('contacts.import.withBasis') : t('contacts.import.withoutConsent'),
    },
  ];

  return (
    <div className="cf-stack">
      <Card title={t('contacts.import.title')} description={t('contacts.import.description')}>
        <form className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
          <FormField
            label={t('contacts.import.file')}
            htmlFor="contacts-import-file"
            hint={t('contacts.import.fileHint')}
          >
            <input
              id="contacts-import-file"
              type="file"
              accept=".csv,text/csv"
              className="cf-input cf-input--file"
              onChange={(e) => void onFile(e)}
            />
          </FormField>
          <FormField
            label={t('contacts.import.csv')}
            htmlFor="contacts-import-csv"
            required
            error={formError}
            hint={t('contacts.import.csvHint', { separator: TAG_SEPARATOR })}
          >
            <Textarea
              id="contacts-import-csv"
              mono
              rows={10}
              value={text}
              onChange={(e) => setText(e.target.value)}
              invalid={Boolean(formError)}
              placeholder={t('contacts.import.placeholder')}
            />
          </FormField>
          {parsed ? (
            <span className="cf-text-sm cf-text-secondary" aria-live="polite">
              {t('contacts.import.detected', {
                n: format.format(parsed.rows.length),
                columns: parsed.columns.join(', '),
              })}
            </span>
          ) : null}
          <div className="cf-form__section">{t('contacts.import.consentSection')}</div>
          <FormField label={t('contacts.import.consent')} htmlFor="contacts-import-consent">
            <Select
              id="contacts-import-consent"
              options={[
                { value: 'none', label: t('contacts.import.consentNone') },
                { value: 'granted', label: t('contacts.import.consentGranted') },
              ]}
              value={consent}
              onChange={(e) => setConsent(e.target.value as ConsentChoice)}
            />
          </FormField>
          {consent === 'granted' ? (
            <FormField
              label={t('contacts.import.basis')}
              htmlFor="contacts-import-basis"
              required
              error={basisError}
              hint={t('contacts.import.basisHint')}
            >
              <Textarea
                id="contacts-import-basis"
                rows={3}
                value={basis}
                onChange={(e) => setBasis(e.target.value)}
                invalid={Boolean(basisError)}
              />
            </FormField>
          ) : null}
          <div className="cf-form__row">
            {canReadLists ? (
              <FormField label={t('contacts.import.list')} htmlFor="contacts-import-list">
                <Select
                  id="contacts-import-list"
                  placeholder={t('contacts.import.noList')}
                  options={(lists.data?.items ?? []).map((l) => ({ value: l.id, label: l.name }))}
                  value={listId}
                  onChange={(e) => setListId(e.target.value)}
                />
              </FormField>
            ) : null}
            <div className="cf-field" style={{ justifyContent: 'flex-end' }}>
              <Checkbox
                label={t('contacts.import.updateExisting')}
                checked={updateExisting}
                onChange={(e) => setUpdateExisting(e.target.checked)}
              />
            </div>
          </div>
          {action.error ? (
            <div className="cf-form__error" role="alert">
              {errorMessage(action.error)}
            </div>
          ) : null}
          {result ? <ImportSummary result={result} /> : null}
          <div className="cf-form__actions">
            <Button type="submit" variant="primary" loading={action.busy}>
              {t('contacts.import.submit')}
            </Button>
          </div>
        </form>
      </Card>
      <Card flush title={t('contacts.import.history')}>
        <DataTable
          columns={historyColumns}
          rows={imports.data?.items ?? []}
          rowKey={(i) => i.id}
          loading={imports.loading}
          error={imports.error}
          onRetry={imports.reload}
          empty={{ title: t('contacts.import.historyEmpty') }}
          onRowClick={setViewing}
          pagination={{
            page: imports.data?.page ?? pager.page,
            perPage: pager.perPage,
            total: imports.data?.total ?? 0,
            totalPages: imports.data?.totalPages ?? 0,
            onPageChange: pager.setPage,
          }}
        />
      </Card>
      {viewing ? (
        <Modal
          open
          size="lg"
          title={t('contacts.import.detailTitle', { date: formatDateTime(viewing.created_at) })}
          onClose={() => setViewing(null)}
          footer={<Button onClick={() => setViewing(null)}>{t('common.close')}</Button>}
        >
          <div className="cf-stack" style={{ gap: 'var(--cf-space-3)' }}>
            <ImportSummary result={viewing} />
            {viewing.consent_basis ? (
              <div className="cf-field">
                <span className="cf-field__label">{t('contacts.import.basis')}</span>
                <pre className="cf-pre">{viewing.consent_basis}</pre>
              </div>
            ) : null}
          </div>
        </Modal>
      ) : null}
    </div>
  );
}

function ImportSummary({ result }: { result: ImportResult }) {
  const format = new Intl.NumberFormat(getLocale());
  const errors = result.errors ?? [];
  return (
    <div className="cf-stack" style={{ gap: 'var(--cf-space-3)' }}>
      <Alert
        tone={result.status === 'completed' ? 'success' : 'danger'}
        title={tEnum('contacts.import.status', result.status)}
      >
        {t('contacts.import.summary', {
          total: format.format(result.total),
          created: format.format(result.created),
          updated: format.format(result.updated),
          skipped: format.format(result.skipped),
        })}
      </Alert>
      {errors.length ? (
        <DataTable
          columns={[
            {
              key: 'line',
              header: t('contacts.import.line'),
              align: 'right',
              width: '80px',
              render: (e) => e.line,
            },
            { key: 'reason', header: t('contacts.import.reason'), render: (e) => e.reason },
          ]}
          rows={errors}
          rowKey={(e) => `${e.line}-${e.reason}`}
        />
      ) : null}
    </div>
  );
}
