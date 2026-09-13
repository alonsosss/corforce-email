import { useEffect, useState } from 'react';
import {
  isRemovable,
  suppressionApi,
  type CreateSuppressionRequest,
  type SuppressionCause,
  type SuppressionEntry,
  type SuppressionMeta,
  type SuppressionReason,
} from '@/api/suppression';
import { ERROR_CODES } from '@/api/errors';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import {
  Badge,
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
import { IconLock, IconPlus, IconTrash } from '@/design/icons';
import { isPast } from '@/lib/datetime';
import { formatDateTime, localToRfc3339 } from '@/lib/format';
import { rules, validateField } from '@/lib/validate';
import { t, tEnum } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { activeCausesAfterRemoving, causeViews } from './causes';
import { ReasonBadge } from './suppressionReason';

const SEARCH_DEBOUNCE_MS = 300;

interface Removal {
  entry: SuppressionEntry;
  cause: SuppressionCause;
  active: boolean;
}

function removalMessage({ entry, cause, active }: Removal): string {
  const vars = { reason: tEnum('suppression.reason', cause.reason), email: entry.email };
  if (!active) return t('suppression.cause.removeConfirmExpired', vars);
  return activeCausesAfterRemoving(entry, cause.id) > 0
    ? t('suppression.cause.removeConfirm', vars)
    : t('suppression.cause.removeConfirmLast', vars);
}

export function EntriesTab({ meta }: { meta: SuppressionMeta }) {
  const toast = useToast();
  const { can } = useAccess();
  const pager = usePagination();
  const [reason, setReason] = useState<SuppressionReason | ''>('');
  const [searchInput, setSearchInput] = useState('');
  const [search, setSearch] = useState('');
  const [creating, setCreating] = useState(false);
  const [removing, setRemoving] = useState<Removal | null>(null);

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
      render: (e) => <strong className="cf-mono cf-break">{e.email}</strong>,
    },
    {
      key: 'causes',
      header: t('suppression.column.causes'),
      render: (e) => (
        <CauseList
          entry={e}
          meta={meta}
          canDelete={canDelete}
          onRemove={(cause, active) => setRemoving({ entry: e, cause, active })}
        />
      ),
    },
    { key: 'created', header: t('common.createdAt'), render: (e) => formatDateTime(e.created_at) },
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
            options={meta.reasons.map((r) => ({
              value: r.reason,
              label: tEnum('suppression.reason', r.reason),
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
          meta={meta}
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
        title={t('suppression.cause.removeTitle')}
        message={removing ? removalMessage(removing) : ''}
        confirmLabel={t('suppression.cause.removeAction')}
        danger
        errorOverrides={{ [ERROR_CODES.UNSUBSCRIBE_PROTECTED]: 'error.unsubscribeProtected' }}
        onCancel={() => setRemoving(null)}
        onConfirm={async () => {
          if (!removing) return;
          await suppressionApi.remove(removing.cause.id);
          toast.success(t('suppression.cause.removed'));
          setRemoving(null);
          entries.reload();
        }}
      />
    </Card>
  );
}

/**
 * Todas las causas de la direccion: la principal destacada, despues las demas vigentes y
 * al final las manuales caducadas. Cada causa se retira por su propio id; el catalogo dice
 * que motivos puede retirar un operador (una baja pedida por la persona, no).
 */
function CauseList({
  entry,
  meta,
  canDelete,
  onRemove,
}: {
  entry: SuppressionEntry;
  meta: SuppressionMeta;
  canDelete: boolean;
  onRemove: (cause: SuppressionCause, active: boolean) => void;
}) {
  return (
    <ul className="cf-cause-list">
      {causeViews(entry).map(({ cause, primary, active }) => {
        const removable = isRemovable(meta, cause.reason);
        const reasonLabel = tEnum('suppression.reason', cause.reason);
        const origin = [cause.source, cause.detail].filter(Boolean).join(' - ');
        return (
          <li
            key={cause.id}
            className={[
              'cf-cause',
              primary ? 'cf-cause--primary' : '',
              active ? '' : 'cf-cause--expired',
            ]
              .filter(Boolean)
              .join(' ')}
          >
            <div className="cf-cause__head">
              <ReasonBadge reason={cause.reason} />
              {primary ? <Badge tone="accent">{t('suppression.cause.primary')}</Badge> : null}
              {cause.expires_at ? (
                <span className="cf-text-sm cf-text-muted">
                  {t(active ? 'suppression.cause.expires' : 'suppression.cause.expired', {
                    date: formatDateTime(cause.expires_at),
                  })}
                </span>
              ) : null}
              {canDelete && removable ? (
                <Button
                  size="sm"
                  variant="ghost"
                  iconOnly
                  icon={<IconTrash size={14} />}
                  onClick={() => onRemove(cause, active)}
                >
                  {t('suppression.cause.remove', { reason: reasonLabel, email: entry.email })}
                </Button>
              ) : null}
              {canDelete && !removable ? (
                <IconLock
                  size={14}
                  className="cf-text-muted"
                  title={t('suppression.cause.protected')}
                />
              ) : null}
            </div>
            {origin ? (
              <span className="cf-text-sm cf-text-secondary cf-break">{origin}</span>
            ) : null}
          </li>
        );
      })}
    </ul>
  );
}

function SuppressionForm({
  meta,
  onClose,
  onCreated,
}: {
  meta: SuppressionMeta;
  onClose: () => void;
  onCreated: () => void;
}) {
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
        validateField(email, rules.required, rules.email, rules.maxLength(meta.max_email_length)) ??
        undefined,
      detail: validateField(detail, rules.maxLength(meta.max_detail_length)) ?? undefined,
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
