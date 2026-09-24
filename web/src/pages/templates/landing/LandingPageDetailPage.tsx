import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { ERROR_CODES } from '@/api/errors';
import {
  pagesApi,
  pagesMeta,
  type PageDetail,
  type PagesMeta,
  type PageVersionSummary,
} from '@/api/pages';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import { useResource } from '@/hooks/useResource';
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
  HtmlPreviewFrame,
  Input,
  Modal,
  PageHeader,
  Skeleton,
  useToast,
  type Column,
} from '@/design/components';
import { IconArchive, IconEdit, IconLayout, IconRefresh, IconTrash } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { changed, isEmptyPatch } from '@/lib/patch';
import { rules, validateField } from '@/lib/validate';
import { t, tEnum } from '@/i18n';
import { paths } from '@/paths';
import { FormModal } from '@/pages/shared/FormModal';
import { ResourceGate } from '@/pages/shared/ResourceGate';
import { RowActions } from '@/pages/shared/RowActions';
import { previewDocument } from './editor/pageContent';
import { NoindexField, SlugField } from './PageCreateForm';
import { PublicUrl } from './PublicUrl';
import { isPublished, pageStatusTone, pageVersionTone } from './pageStatus';
import { slugError } from './slug';

type Dialog = 'edit' | 'archive' | 'restore' | 'delete' | 'unpublish' | null;

export default function LandingPageDetailPage() {
  const { id = '' } = useParams();
  const navigate = useNavigate();
  const toast = useToast();
  const { can } = useAccess();
  const [dialog, setDialog] = useState<Dialog>(null);
  const detail = useQuery(() => pagesApi.get(id), [id]);

  if (detail.error) {
    return (
      <div>
        <PageHeader
          title={t('templates.pages.title')}
          back={{ to: paths.landingPages, label: t('nav.landingPages') }}
        />
        <Card>
          <ErrorState
            error={detail.error}
            title={t('templates.pages.notFound')}
            onRetry={detail.reload}
          />
        </Card>
      </div>
    );
  }
  if (!detail.data) {
    return (
      <Card>
        <Skeleton lines={6} />
      </Card>
    );
  }

  const { page } = detail.data;
  const archived = page.status === 'archived';
  const published = isPublished(page);
  const canUpdate = can(...PERMISSIONS.landingPages.update);
  const canPublish = can(...PERMISSIONS.landingPages.publish);
  const close = () => setDialog(null);

  const setStatus = async (status: 'active' | 'archived') => {
    await pagesApi.update(page.id, { status });
    toast.success(
      status === 'archived' ? t('templates.pages.archived') : t('templates.pages.restored'),
    );
    close();
    detail.reload();
  };

  return (
    <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
      <PageHeader
        title={page.name}
        description={
          <span className="cf-inline">
            <Badge tone={pageStatusTone(page.status)}>
              {tEnum('templates.pages.status', page.status)}
            </Badge>
            <span>
              {published
                ? t('templates.publishedVersion', { n: page.current_version })
                : t('templates.pages.unpublished')}
            </span>
          </span>
        }
        back={{ to: paths.landingPages, label: t('nav.landingPages') }}
        actions={
          <>
            {can(...PERMISSIONS.landingPages.create) && !archived ? (
              <Button
                variant="primary"
                icon={<IconLayout size={16} />}
                onClick={() => navigate(paths.landingPageEditor(page.id))}
              >
                {t('templates.pages.openEditor')}
              </Button>
            ) : null}
            {canUpdate ? (
              <Button icon={<IconEdit size={16} />} onClick={() => setDialog('edit')}>
                {t('common.edit')}
              </Button>
            ) : null}
            {canPublish && published && !archived ? (
              <Button onClick={() => setDialog('unpublish')}>
                {t('templates.pages.unpublish')}
              </Button>
            ) : null}
            {canUpdate ? (
              archived ? (
                <Button icon={<IconRefresh size={16} />} onClick={() => setDialog('restore')}>
                  {t('templates.restore')}
                </Button>
              ) : (
                <Button icon={<IconArchive size={16} />} onClick={() => setDialog('archive')}>
                  {t('templates.archive')}
                </Button>
              )
            ) : null}
            {archived && can(...PERMISSIONS.landingPages.delete) ? (
              <Button
                variant="danger"
                icon={<IconTrash size={16} />}
                onClick={() => setDialog('delete')}
              >
                {t('common.delete')}
              </Button>
            ) : null}
          </>
        }
      />
      {archived ? <Alert tone="warning">{t('templates.pages.archivedNotice')}</Alert> : null}
      <Card title={t('templates.pages.summary')}>
        <DescriptionList
          items={[
            { label: t('templates.pages.publicUrl'), value: <PublicUrl page={page} copyable /> },
            {
              label: t('templates.pages.slug'),
              value: <span className="cf-mono">{page.slug}</span>,
            },
            {
              label: t('templates.pages.indexing'),
              value: page.noindex
                ? t('templates.pages.noindexOn')
                : t('templates.pages.noindexOff'),
            },
            { label: t('common.updatedAt'), value: formatDateTime(page.updated_at) },
          ]}
        />
      </Card>
      <VersionsCard detail={detail.data} onChanged={detail.reload} />

      {dialog === 'edit' ? (
        <PageEditForm
          detail={detail.data}
          onClose={close}
          onSaved={() => {
            toast.success(t('templates.pages.updated'));
            close();
            detail.reload();
          }}
        />
      ) : null}
      <ConfirmDialog
        open={dialog === 'unpublish'}
        title={t('templates.pages.unpublish')}
        message={t('templates.pages.unpublishConfirm', { name: page.name })}
        confirmLabel={t('templates.pages.unpublish')}
        danger
        onCancel={close}
        onConfirm={async () => {
          await pagesApi.unpublish(page.id);
          toast.success(t('templates.pages.unpublishDone'));
          close();
          detail.reload();
        }}
      />
      <ConfirmDialog
        open={dialog === 'archive'}
        title={t('templates.archive')}
        message={t('templates.pages.archiveConfirm', { name: page.name })}
        confirmLabel={t('templates.archive')}
        onCancel={close}
        onConfirm={() => setStatus('archived')}
      />
      <ConfirmDialog
        open={dialog === 'restore'}
        title={t('templates.restore')}
        message={t('templates.pages.restoreConfirm', { name: page.name })}
        confirmLabel={t('templates.restore')}
        onCancel={close}
        onConfirm={() => setStatus('active')}
      />
      <ConfirmDialog
        open={dialog === 'delete'}
        title={t('templates.pages.delete')}
        message={t('templates.pages.deleteConfirm', { name: page.name })}
        confirmLabel={t('common.delete')}
        danger
        errorOverrides={{ [ERROR_CODES.CONFLICT]: 'templates.pages.deleteNotArchived' }}
        onCancel={close}
        onConfirm={async () => {
          await pagesApi.remove(page.id);
          toast.success(t('templates.pages.deleted'));
          navigate(paths.landingPages, { replace: true });
        }}
      />
    </div>
  );
}

function VersionsCard({ detail, onChanged }: { detail: PageDetail; onChanged: () => void }) {
  const toast = useToast();
  const { can } = useAccess();
  const { page } = detail;
  const [viewing, setViewing] = useState<number | null>(null);
  const [publishing, setPublishing] = useState<PageVersionSummary | null>(null);
  const canPublish = can(...PERMISSIONS.landingPages.publish) && page.status !== 'archived';
  const versions = [...detail.versions].sort((a, b) => b.version - a.version);

  const columns: Column<PageVersionSummary>[] = [
    {
      key: 'version',
      header: t('templates.versions.column.version'),
      render: (v) => (
        <span className="cf-inline">
          <strong>{t('templates.versionLabel', { n: v.version })}</strong>
          {v.version === page.current_version ? (
            <Badge tone="accent">{t('templates.pages.live')}</Badge>
          ) : null}
        </span>
      ),
    },
    {
      key: 'status',
      header: t('common.status'),
      render: (v) => (
        <Badge tone={pageVersionTone(v.status)}>{tEnum('templates.versionStatus', v.status)}</Badge>
      ),
    },
    {
      key: 'published',
      header: t('templates.versions.column.publishedAt'),
      render: (v) => formatDateTime(v.published_at),
    },
    { key: 'created', header: t('common.createdAt'), render: (v) => formatDateTime(v.created_at) },
    {
      key: 'actions',
      header: '',
      align: 'right',
      render: (v) => (
        <RowActions>
          <Button size="sm" variant="ghost" onClick={() => setViewing(v.version)}>
            {t('templates.versions.view')}
          </Button>
          {canPublish && v.status === 'draft' ? (
            <Button size="sm" variant="primary" onClick={() => setPublishing(v)}>
              {t('templates.versions.publish')}
            </Button>
          ) : null}
        </RowActions>
      ),
    },
  ];

  return (
    <Card
      flush
      title={t('templates.versions.title')}
      description={t('templates.pages.versionsHint')}
    >
      <DataTable
        columns={columns}
        rows={versions}
        rowKey={(v) => v.id}
        empty={{ title: t('templates.pages.noVersions') }}
      />
      {viewing !== null ? (
        <VersionViewer pageId={page.id} version={viewing} onClose={() => setViewing(null)} />
      ) : null}
      <ConfirmDialog
        open={publishing !== null}
        title={t('templates.versions.publish')}
        message={
          publishing
            ? isPublished(page)
              ? t('templates.pages.publishReplace', {
                  n: publishing.version,
                  current: page.current_version,
                })
              : t('templates.pages.publishFirst', { n: publishing.version })
            : ''
        }
        confirmLabel={t('templates.versions.publish')}
        onCancel={() => setPublishing(null)}
        onConfirm={async () => {
          if (!publishing) return;
          await pagesApi.publish(page.id, publishing.version);
          toast.success(t('templates.versions.published', { n: publishing.version }));
          setPublishing(null);
          onChanged();
        }}
      />
    </Card>
  );
}

function VersionViewer({
  pageId,
  version,
  onClose,
}: {
  pageId: string;
  version: number;
  onClose: () => void;
}) {
  const content = useQuery(() => pagesApi.getVersion(pageId, version), [pageId, version]);
  const v = content.data;
  return (
    <Modal
      open
      size="lg"
      title={t('templates.versionLabel', { n: version })}
      onClose={onClose}
      footer={<Button onClick={onClose}>{t('common.close')}</Button>}
    >
      {content.error ? (
        <ErrorState error={content.error} onRetry={content.reload} />
      ) : !v ? (
        <Skeleton lines={6} />
      ) : (
        <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
          <DescriptionList
            items={[
              { label: t('templates.pages.documentTitle'), value: v.title },
              { label: t('common.description'), value: v.description || t('common.dash') },
              {
                label: t('common.status'),
                value: (
                  <Badge tone={pageVersionTone(v.status)}>
                    {tEnum('templates.versionStatus', v.status)}
                  </Badge>
                ),
              },
              {
                label: t('templates.versions.column.publishedAt'),
                value: formatDateTime(v.published_at),
              },
            ]}
          />
          <HtmlPreviewFrame
            html={previewDocument(v)}
            title={t('templates.pages.preview')}
            height={480}
          />
        </div>
      )}
    </Modal>
  );
}

interface PageEditFormProps {
  detail: PageDetail;
  onClose: () => void;
  onSaved: () => void;
}

function PageEditForm(props: PageEditFormProps) {
  const meta = useResource(pagesMeta);
  return (
    <ResourceGate
      resource={meta}
      modal={{ title: t('templates.pages.form.editTitle'), onClose: props.onClose }}
    >
      {(data) => <EditForm {...props} meta={data} />}
    </ResourceGate>
  );
}

function EditForm({ detail, onClose, onSaved, meta }: PageEditFormProps & { meta: PagesMeta }) {
  const { page } = detail;
  const [name, setName] = useState(page.name);
  const [slug, setSlug] = useState(page.slug);
  const [noindex, setNoindex] = useState(page.noindex);
  const [errors, setErrors] = useState<{ name?: string; slug?: string }>({});

  const action = useAction(async () => {
    const body = {
      name: changed(name.trim(), page.name),
      slug: changed(slug, page.slug),
      noindex: changed(noindex, page.noindex),
    };
    if (isEmptyPatch(body)) {
      onClose();
      return;
    }
    await pagesApi.update(page.id, body);
    onSaved();
  });

  const submit = async () => {
    const next = {
      name: validateField(name, rules.required, rules.maxLength(meta.max_name_length)) ?? undefined,
      slug: slugError(slug, meta) ?? undefined,
    };
    setErrors(next);
    if (next.name || next.slug) return;
    await action.run();
  };

  return (
    <FormModal
      id="page-edit-form"
      title={t('templates.pages.form.editTitle')}
      submitLabel={t('common.save')}
      busy={action.busy}
      error={action.error}
      errorOverrides={{ [ERROR_CODES.CONFLICT]: 'templates.pages.exists' }}
      onClose={onClose}
      onSubmit={submit}
    >
      <FormField label={t('common.name')} htmlFor="page-edit-name" required error={errors.name}>
        <Input
          id="page-edit-name"
          value={name}
          maxLength={meta.max_name_length}
          onChange={(e) => setName(e.target.value)}
          invalid={Boolean(errors.name)}
        />
      </FormField>
      <SlugField
        id="page-edit-slug"
        value={slug}
        meta={meta}
        error={errors.slug}
        onChange={setSlug}
      />
      {slug !== page.slug && isPublished(page) ? (
        <Alert tone="warning">{t('templates.pages.slugChangeWarning')}</Alert>
      ) : null}
      <NoindexField checked={noindex} onChange={setNoindex} />
    </FormModal>
  );
}
