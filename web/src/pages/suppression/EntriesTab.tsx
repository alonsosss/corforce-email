import { useEffect, useState } from 'react';
import {
  SUPPRESSION_REASONS,
  isRemovableReason,
  suppressionApi,
  type CreateSuppressionRequest,
  type SuppressionEntry,
  type SuppressionReason,
} from '@/api/suppression';
import { ERROR_CODES } from '@/api/errors';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import {
  Button,
  Card,
  ConfirmDialog,
  DataTable,
  FormField,
  Input,
  Select,
  useToast,
  type Column,
} from '@/design/components';
import { IconPlus } from '@/design/icons';
import { isPast } from '@/lib/datetime';
import { formatDateTime, localToRfc3339 } from '@/lib/format';
import { rules, validateField } from '@/lib/validate';
import { t, tEnum } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { RowActions } from '@/pages/shared/RowActions';
import { ReasonBadge } from './suppressionReason';

const SEARCH_DEBOUNCE_MS = 300;
const EMAIL_MAX_LENGTH = 320;
const DETAIL_MAX_LENGTH = 1000;

export function EntriesTab() {
  const toast = useToast();
  const { can } = useAccess();
  const pager = usePagination();
  const [reason, setReason] = useState<SuppressionReason | ''>('');
  const [searchInput, setSearchInput] = useState('');
  const [search, setSearch] = useState('');
  const [creating, setCreating] = useState(false);
  const [removing, setRemoving] = useState<SuppressionEntry | null>(null);

  useEffect(() => {
    const handle = window.setTimeout(() => {
      setSearch(searchInput.trim());
      pager.reset();
    }, SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(handle);
    // pager.reset es estable (useCallback); solo el texto dispara la busqueda.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [searchInput]);

  const entries = useQuery(
    () =>
      suppressionApi.list({
        page: pager.page,
        per_page: pager.perPage,
        reason: reason || undefined,
        search: search || undefined,
      }),
    [pager.page, pager.perPage, reason, search],
  );

  const canDelete = can(...PERMISSIONS.suppressionEntries.delete);

  const columns: Column<SuppressionEntry>[] = [
    {
      key: 'email',
      header: t('common.email'),
      render: (e) => <strong className="cf-mono">{e.email}</strong>,
    },
    {
      key: 'reason',
      header: t('suppression.column.reason'),
      render: (e) => <ReasonBadge reason={e.reason} />,
    },
    {
      key: 'source',
      header: t('suppression.column.source'),
      render: (e) => e.source || t('common.dash'),
    },
    {
      key: 'detail',
      header: t('suppression.column.detail'),
      render: (e) => (e.detail ? <span className="cf-text-sm">{e.detail}</span> : t('common.dash')),
    },
    {
      key: 'expires',
      header: t('suppression.column.expires'),
      render: (e) => (e.expires_at ? formatDateTime(e.expires_at) : t('suppression.noExpiry')),
    },
    { key: 'created', header: t('common.createdAt'), render: (e) => formatDateTime(e.created_at) },
    {
      key: 'actions',
      header: '',
      align: 'right',
      render: (e) => (
        <RowActions
          onDelete={canDelete && isRemovableReason(e.reason) ? () => setRemoving(e) : undefined}
        />
      ),
    },
  ];

  return (
    <Card
      flush
      title={t('suppression.entries.title')}
      description={t('suppression.entries.description')}
      actions={
        can(...PERMISSIONS.suppressionEntries.create) ? (
          <Button variant="primary" icon={<IconPlus size={16} />} onClick={() => setCreating(true)}>
            {t('suppression.new')}
          </Button>
        ) : null
      }
    >
      <div className="cf-toolbar">
        <div className="cf-field">
          <label className="cf-field__label" htmlFor="suppression-search">
            {t('common.search')}
          </label>
          <Input
            id="suppression-search"
            placeholder={t('suppression.searchPlaceholder')}
            value={searchInput}
            onChange={(e) => setSearchInput(e.target.value)}
          />
        </div>
        <div className="cf-field">
          <label className="cf-field__label" htmlFor="suppression-reason">
            {t('suppression.column.reason')}
          </label>
          <Select
            id="suppression-reason"
            placeholder={t('common.all')}
            options={SUPPRESSION_REASONS.map((r) => ({
              value: r,
              label: tEnum('suppression.reason', r),
            }))}
            value={reason}
            onChange={(e) => {
              setReason(e.target.value as SuppressionReason | '');
              pager.reset();
            }}
          />
        </div>
      </div>
      <DataTable
        columns={columns}
        rows={entries.data?.items ?? []}
        rowKey={(e) => e.id}
        loading={entries.loading}
        error={entries.error}
        onRetry={entries.reload}
        empty={{ title: t('suppression.empty') }}
        pagination={{
          page: entries.data?.page ?? pager.page,
          perPage: pager.perPage,
          total: entries.data?.total ?? 0,
          totalPages: entries.data?.totalPages ?? 0,
          onPageChange: pager.setPage,
        }}
      />
      {creating ? (
        <SuppressionForm
          onClose={() => setCreating(false)}
          onCreated={() => {
            toast.success(t('suppression.created'));
            setCreating(false);
            entries.reload();
          }}
        />
      ) : null}
      <ConfirmDialog
        open={removing !== null}
        title={t('suppression.delete')}
        message={t('suppression.deleteConfirm', { email: removing?.email ?? '' })}
        confirmLabel={t('suppression.delete')}
        danger
        errorOverrides={{ [ERROR_CODES.UNSUBSCRIBE_PROTECTED]: 'error.unsubscribeProtected' }}
        onCancel={() => setRemoving(null)}
        onConfirm={async () => {
          if (!removing) return;
          await suppressionApi.remove(removing.id);
          toast.success(t('suppression.deleted'));
          setRemoving(null);
          entries.reload();
        }}
      />
    </Card>
  );
}

function SuppressionForm({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [email, setEmail] = useState('');
  const [detail, setDetail] = useState('');
  const [expires, setExpires] = useState('');
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});

  const action = useAction(async (input: CreateSuppressionRequest) => {
    await suppressionApi.create(input);
    onCreated();
  });

  const submit = async () => {
    const expiresAt = localToRfc3339(expires);
    const next = {
      email:
        validateField(email, rules.required, rules.email, rules.maxLength(EMAIL_MAX_LENGTH)) ??
        undefined,
      detail: validateField(detail, rules.maxLength(DETAIL_MAX_LENGTH)) ?? undefined,
      expires: expiresAt && isPast(expiresAt) ? t('suppression.form.expiryPast') : undefined,
    };
    setErrors(next);
    if (Object.values(next).some(Boolean)) return;
    await action.run({ email: email.trim(), detail: detail.trim(), expires_at: expiresAt });
  };

  return (
    <FormModal
      id="suppression-form"
      title={t('suppression.form.title')}
      submitLabel={t('common.create')}
      busy={action.busy}
      error={action.error}
      errorOverrides={{ [ERROR_CODES.CONFLICT]: 'suppression.exists' }}
      onClose={onClose}
      onSubmit={submit}
    >
      <FormField
        label={t('common.email')}
        htmlFor="suppression-email"
        required
        error={errors.email}
      >
        <Input
          id="suppression-email"
          type="email"
          className="cf-mono"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          invalid={Boolean(errors.email)}
          autoComplete="off"
        />
      </FormField>
      <FormField
        label={t('suppression.column.detail')}
        htmlFor="suppression-detail"
        error={errors.detail}
      >
        <Input id="suppression-detail" value={detail} onChange={(e) => setDetail(e.target.value)} />
      </FormField>
      <FormField
        label={t('suppression.column.expires')}
        htmlFor="suppression-expires"
        error={errors.expires}
        hint={t('suppression.form.expiryHint')}
      >
        <Input
          id="suppression-expires"
          type="datetime-local"
          value={expires}
          onChange={(e) => setExpires(e.target.value)}
          invalid={Boolean(errors.expires)}
        />
      </FormField>
    </FormModal>
  );
}
