import { useMemo, useState, type FormEvent } from 'react';
import {
  suppressionApi,
  type SuppressionMeta,
  type ImportResult,
  type ImportSuppressionRequest,
  type SuppressionImport,
} from '@/api/suppression';
import { errorMessage } from '@/api/messages';
import { useAction } from '@/hooks/useAction';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import {
  Alert,
  Button,
  Card,
  DataTable,
  FormField,
  Input,
  Textarea,
  useToast,
  type Column,
} from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { splitLines } from '@/lib/listInput';
import { rules, validateField } from '@/lib/validate';
import { getLocale, t } from '@/i18n';

export function ImportTab({ meta }: { meta: SuppressionMeta }) {
  const toast = useToast();
  const pager = usePagination();
  const [text, setText] = useState('');
  const [detail, setDetail] = useState('');
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});
  const [result, setResult] = useState<ImportResult | null>(null);

  const lines = useMemo(() => splitLines(text), [text]);
  const format = new Intl.NumberFormat(getLocale());

  const imports = useQuery(
    () => suppressionApi.listImports({ page: pager.page, per_page: pager.perPage }),
    [pager.page, pager.perPage],
  );

  const action = useAction(async (input: ImportSuppressionRequest) => {
    const { data } = await suppressionApi.importEmails(input);
    setResult(data);
    setText('');
    toast.success(t('suppression.import.done'));
    imports.reload();
  });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const next = {
      emails:
        lines.length === 0
          ? t('suppression.import.emptyList')
          : lines.length > meta.max_import_emails
            ? t('suppression.import.tooMany', { max: format.format(meta.max_import_emails) })
            : undefined,
      detail: validateField(detail, rules.maxLength(meta.max_detail_length)) ?? undefined,
    };
    setErrors(next);
    if (next.emails || next.detail) return;
    setResult(null);
    await action.run({ emails: lines, detail: detail.trim() });
  };

  const columns: Column<SuppressionImport>[] = [
    { key: 'when', header: t('common.createdAt'), render: (i) => formatDateTime(i.created_at) },
    {
      key: 'total',
      header: t('suppression.import.total'),
      align: 'right',
      render: (i) => format.format(i.total),
    },
    {
      key: 'added',
      header: t('suppression.import.added'),
      align: 'right',
      render: (i) => format.format(i.added),
    },
    {
      key: 'skipped',
      header: t('suppression.import.skipped'),
      align: 'right',
      render: (i) => format.format(i.skipped),
    },
    {
      key: 'by',
      header: t('suppression.import.createdBy'),
      render: (i) => <span className="cf-mono cf-text-sm">{i.created_by}</span>,
    },
  ];

  return (
    <div className="cf-stack">
      <Card title={t('suppression.import.title')} description={t('suppression.import.description')}>
        <form className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
          <FormField
            label={t('suppression.import.addresses')}
            htmlFor="suppression-import"
            required
            error={errors.emails}
            hint={t('suppression.import.count', {
              n: format.format(lines.length),
              max: format.format(meta.max_import_emails),
            })}
          >
            <Textarea
              id="suppression-import"
              mono
              rows={12}
              value={text}
              onChange={(e) => setText(e.target.value)}
              invalid={Boolean(errors.emails)}
              placeholder={t('suppression.import.placeholder')}
            />
          </FormField>
          <FormField
            label={t('suppression.column.detail')}
            htmlFor="suppression-import-detail"
            error={errors.detail}
          >
            <Input
              id="suppression-import-detail"
              value={detail}
              onChange={(e) => setDetail(e.target.value)}
            />
          </FormField>
          {action.error ? (
            <div className="cf-form__error" role="alert">
              {errorMessage(action.error)}
            </div>
          ) : null}
          {result ? (
            <Alert tone="success" title={t('suppression.import.done')}>
              {t('suppression.import.summary', {
                total: format.format(result.total),
                added: format.format(result.added),
                skipped: format.format(result.skipped),
              })}
            </Alert>
          ) : null}
          <div className="cf-form__actions">
            <Button type="submit" variant="primary" loading={action.busy}>
              {t('suppression.import.submit')}
            </Button>
          </div>
        </form>
      </Card>
      <Card flush title={t('suppression.import.history')}>
        <DataTable
          columns={columns}
          rows={imports.data?.items ?? []}
          rowKey={(i) => i.id}
          loading={imports.loading}
          error={imports.error}
          onRetry={imports.reload}
          empty={{ title: t('suppression.import.historyEmpty') }}
          pagination={{
            page: imports.data?.page ?? pager.page,
            perPage: pager.perPage,
            total: imports.data?.total ?? 0,
            totalPages: imports.data?.totalPages ?? 0,
            onPageChange: pager.setPage,
          }}
        />
      </Card>
    </div>
  );
}
