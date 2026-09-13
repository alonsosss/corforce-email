import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import type { Contact } from '@/api/contacts';
import { contactsApi } from '@/api/contacts';
import { PICKER_PAGE_SIZE } from '@/api/paging';
import {
  segmentsApi,
  type Segment,
  type SegmentCatalog,
  type SegmentPreview,
} from '@/api/segments';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import {
  Alert,
  Button,
  Card,
  ConfirmDialog,
  DataTable,
  ErrorState,
  FormField,
  Input,
  PageHeader,
  Skeleton,
  Textarea,
  useToast,
  type Column,
} from '@/design/components';
import { IconEye, IconTrash } from '@/design/icons';
import { errorMessage } from '@/api/messages';
import { fullName } from '@/lib/format';
import { rules, validateField } from '@/lib/validate';
import { getLocale, t } from '@/i18n';
import { paths } from '@/paths';
import { ConsentBadge, ContactStatusBadge } from '@/pages/contacts/contactStatus';
import { SegmentEditor, type ListOption } from './SegmentEditor';
import { fromDefinition, newGroup, toDefinition, type DraftGroup } from './segmentDraft';

export default function SegmentEditorPage() {
  const { id } = useParams();
  const { can } = useAccess();
  const canReadLists = can(...PERMISSIONS.contactLists.read);
  const catalog = useQuery(() => segmentsApi.meta(), []);
  const segment = useQuery(async () => (id ? (await segmentsApi.get(id)).data : null), [id]);
  const lists = useQuery(
    async (): Promise<ListOption[] | null> =>
      canReadLists
        ? (await contactsApi.listLists({ page: 1, per_page: PICKER_PAGE_SIZE })).items
        : null,
    [canReadLists],
  );

  const failed = catalog.error ?? segment.error;
  if (failed) {
    return (
      <div>
        <PageHeader
          title={t('segments.title')}
          back={{ to: paths.segments, label: t('nav.segments') }}
        />
        <Card>
          <ErrorState
            error={failed}
            title={segment.error ? t('segments.notFound') : undefined}
            onRetry={() => {
              catalog.reload();
              segment.reload();
            }}
          />
        </Card>
      </div>
    );
  }
  if (!catalog.data || segment.loading || lists.loading) {
    return (
      <Card>
        <Skeleton lines={8} />
      </Card>
    );
  }
  return (
    <SegmentForm
      key={segment.data?.id ?? 'new'}
      catalog={catalog.data}
      segment={segment.data}
      lists={lists.data}
    />
  );
}

function SegmentForm({
  catalog,
  segment,
  lists,
}: {
  catalog: SegmentCatalog;
  segment: Segment | null;
  lists: ListOption[] | null;
}) {
  const navigate = useNavigate();
  const toast = useToast();
  const { can } = useAccess();
  const [name, setName] = useState(segment?.name ?? '');
  const [description, setDescription] = useState(segment?.description ?? '');
  const [draft, setDraft] = useState<DraftGroup>(() =>
    segment ? fromDefinition(segment.definition) : newGroup(catalog),
  );
  const [nameError, setNameError] = useState<string | null>(null);
  const [ruleErrors, setRuleErrors] = useState<Record<string, string>>({});
  const [preview, setPreview] = useState<SegmentPreview | null>(null);
  const [deleting, setDeleting] = useState(false);

  const canSave = segment
    ? can(...PERMISSIONS.segments.update)
    : can(...PERMISSIONS.segments.create);
  const canPreview = can(...PERMISSIONS.segments.preview);

  const build = () => {
    const built = toDefinition(draft, catalog);
    setRuleErrors(built.errors);
    return built.definition;
  };

  const save = useAction(async () => {
    const error = validateField(name, rules.required);
    setNameError(error);
    const definition = build();
    if (error || !definition) return;
    if (segment) {
      await segmentsApi.update(segment.id, {
        name: name.trim(),
        description: description.trim(),
        definition,
      });
      toast.success(t('segments.updated'));
    } else {
      const { data } = await segmentsApi.create({
        name: name.trim(),
        description: description.trim(),
        definition,
      });
      toast.success(t('segments.created'));
      navigate(paths.segment(data.id), { replace: true });
    }
  });

  const runPreview = useAction(async () => {
    const definition = build();
    if (!definition) return;
    setPreview(await segmentsApi.preview(definition));
  });

  return (
    <div>
      <PageHeader
        title={segment ? segment.name : t('segments.new')}
        description={t('segments.editor.description')}
        back={{ to: paths.segments, label: t('nav.segments') }}
        actions={
          <>
            {canSave ? (
              <Button variant="primary" loading={save.busy} onClick={() => void save.run()}>
                {segment ? t('common.save') : t('common.create')}
              </Button>
            ) : null}
            {segment && can(...PERMISSIONS.segments.delete) ? (
              <Button
                variant="danger"
                icon={<IconTrash size={16} />}
                onClick={() => setDeleting(true)}
              >
                {t('common.delete')}
              </Button>
            ) : null}
          </>
        }
      />
      <div className="cf-stack">
        {save.error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(save.error)}
          </div>
        ) : null}
        <Card title={t('segments.editor.data')}>
          <div className="cf-form">
            <FormField label={t('common.name')} htmlFor="segment-name" required error={nameError}>
              <Input
                id="segment-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                invalid={Boolean(nameError)}
                readOnly={!canSave}
              />
            </FormField>
            <FormField label={t('common.description')} htmlFor="segment-description">
              <Textarea
                id="segment-description"
                rows={2}
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                readOnly={!canSave}
              />
            </FormField>
          </div>
        </Card>
        <Card title={t('segments.editor.rules')} description={t('segments.editor.rulesHint')}>
          <SegmentEditor
            catalog={catalog}
            value={draft}
            onChange={(next) => {
              setDraft(next);
              setPreview(null);
            }}
            errors={ruleErrors}
            lists={lists}
            disabled={!canSave}
          />
        </Card>
        {canPreview ? (
          <PreviewCard
            preview={preview}
            busy={runPreview.busy}
            error={runPreview.error}
            onRun={() => void runPreview.run()}
          />
        ) : null}
        {segment ? <MembersCard segmentId={segment.id} version={segment.updated_at} /> : null}
      </div>
      <ConfirmDialog
        open={deleting}
        title={t('segments.delete')}
        message={t('segments.deleteConfirm', { name: segment?.name ?? '' })}
        confirmLabel={t('common.delete')}
        danger
        onCancel={() => setDeleting(false)}
        onConfirm={async () => {
          if (!segment) return;
          await segmentsApi.remove(segment.id);
          toast.success(t('segments.deleted'));
          navigate(paths.segments, { replace: true });
        }}
      />
    </div>
  );
}

function contactColumns(): Column<Contact>[] {
  return [
    {
      key: 'email',
      header: t('common.email'),
      render: (c) => (
        <div className="cf-cell-stack">
          <strong className="cf-mono cf-break">{c.email}</strong>
          {fullName(c.first_name, c.last_name) ? (
            <span className="cf-text-muted cf-text-sm">{fullName(c.first_name, c.last_name)}</span>
          ) : null}
        </div>
      ),
    },
    {
      key: 'status',
      header: t('common.status'),
      render: (c) => <ContactStatusBadge status={c.status} />,
    },
    {
      key: 'consent',
      header: t('contacts.column.consent'),
      render: (c) => <ConsentBadge status={c.consent_status} />,
    },
  ];
}

function PreviewCard({
  preview,
  busy,
  error,
  onRun,
}: {
  preview: SegmentPreview | null;
  busy: boolean;
  error: unknown;
  onRun: () => void;
}) {
  const format = new Intl.NumberFormat(getLocale());
  return (
    <Card
      flush
      title={t('segments.preview.title')}
      description={t('segments.preview.description')}
      actions={
        <Button icon={<IconEye size={16} />} loading={busy} onClick={onRun}>
          {t('segments.preview.run')}
        </Button>
      }
    >
      {error ? (
        <div className="cf-table__state">
          <div className="cf-form__error" role="alert">
            {errorMessage(error)}
          </div>
        </div>
      ) : null}
      {preview ? (
        <>
          <div style={{ padding: 'var(--cf-space-4) var(--cf-space-5)' }}>
            <Alert tone="info">
              {t('segments.preview.count', { n: format.format(preview.count) })}
            </Alert>
          </div>
          <DataTable
            columns={contactColumns()}
            rows={preview.sample}
            rowKey={(c) => c.id}
            empty={{ title: t('segments.preview.empty') }}
          />
        </>
      ) : !error ? (
        <div className="cf-table__state">
          <span className="cf-text-muted cf-text-sm">{t('segments.preview.idle')}</span>
        </div>
      ) : null}
    </Card>
  );
}

function MembersCard({ segmentId, version }: { segmentId: string; version: string }) {
  const navigate = useNavigate();
  const pager = usePagination();
  const members = useQuery(
    () => segmentsApi.contacts(segmentId, { page: pager.page, per_page: pager.perPage }),
    [segmentId, version, pager.page, pager.perPage],
  );
  return (
    <Card flush title={t('segments.members.title')} description={t('segments.members.description')}>
      <DataTable
        columns={contactColumns()}
        rows={members.data?.items ?? []}
        rowKey={(c) => c.id}
        loading={members.loading}
        error={members.error}
        onRetry={members.reload}
        empty={{ title: t('segments.members.empty') }}
        onRowClick={(c) => navigate(paths.contact(c.id))}
        pagination={{
          page: members.data?.page ?? pager.page,
          perPage: pager.perPage,
          total: members.data?.total ?? 0,
          totalPages: members.data?.totalPages ?? 0,
          onPageChange: pager.setPage,
        }}
      />
    </Card>
  );
}
