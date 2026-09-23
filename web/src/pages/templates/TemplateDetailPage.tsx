import { useState } from 'react';
import { useNavigate, useParams, useSearchParams } from 'react-router-dom';
import {
  templatesApi,
  templatesMeta,
  type TemplateDetail,
  type TemplatesMeta,
} from '@/api/templates';
import { ERROR_CODES } from '@/api/errors';
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
  ErrorState,
  FormField,
  Input,
  PageHeader,
  Skeleton,
  Tabs,
  Textarea,
  useToast,
} from '@/design/components';
import { IconArchive, IconEdit, IconLayers, IconRefresh, IconTrash } from '@/design/icons';
import { changed, isEmptyPatch } from '@/lib/patch';
import { rules, validateField } from '@/lib/validate';
import { t, tEnum } from '@/i18n';
import { paths } from '@/paths';
import { FormModal } from '@/pages/shared/FormModal';
import { ResourceGate } from '@/pages/shared/ResourceGate';
import { PreviewTab } from './PreviewTab';
import { templateKindTone, templateStatusTone } from './templateStatus';
import { VersionsTab } from './VersionsTab';

type TabId = 'versions' | 'preview';
type Dialog = 'edit' | 'archive' | 'restore' | 'delete' | null;

export default function TemplateDetailPage() {
  const { id = '' } = useParams();
  const navigate = useNavigate();
  const toast = useToast();
  const { can } = useAccess();
  const [, setParams] = useSearchParams();
  const [dialog, setDialog] = useState<Dialog>(null);
  const detail = useQuery(async () => (await templatesApi.get(id)).data, [id]);

  const tabs: { id: TabId; label: string }[] = [
    { id: 'versions', label: t('templates.tab.versions') },
    ...(can(...PERMISSIONS.templates.render)
      ? [{ id: 'preview' as const, label: t('templates.tab.preview') }]
      : []),
  ];
  const [tab, setTab] = useTabParam(
    tabs.map((item) => item.id),
    'versions',
  );

  if (detail.error) {
    return (
      <div>
        <PageHeader
          title={t('templates.title')}
          back={{ to: paths.templates, label: t('nav.templates') }}
        />
        <Card>
          <ErrorState
            error={detail.error}
            title={t('templates.notFound')}
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

  const tpl = detail.data;
  const archived = tpl.status === 'archived';
  const canUpdate = can(...PERMISSIONS.templates.update);
  const close = () => setDialog(null);

  const openPreview = (version: number) =>
    setParams((prev) => {
      const next = new URLSearchParams(prev);
      next.set('tab', 'preview');
      next.set('version', String(version));
      return next;
    });

  const setStatus = async (status: 'active' | 'archived') => {
    await templatesApi.update(tpl.id, { status });
    toast.success(status === 'archived' ? t('templates.archived') : t('templates.restored'));
    close();
    detail.reload();
  };

  return (
    <div>
      <PageHeader
        title={tpl.name}
        description={
          <span className="cf-inline">
            <Badge tone={templateKindTone(tpl.kind)}>{tEnum('templates.kind', tpl.kind)}</Badge>
            <Badge tone={templateStatusTone(tpl.status)}>
              {tEnum('templates.status', tpl.status)}
            </Badge>
            <span>
              {tpl.current_version > 0
                ? t('templates.publishedVersion', { n: tpl.current_version })
                : t('templates.unpublished')}
            </span>
          </span>
        }
        back={{ to: paths.templates, label: t('nav.templates') }}
        actions={
          <>
            {can(...PERMISSIONS.templates.create) && !archived ? (
              <Button
                variant="primary"
                icon={<IconLayers size={16} />}
                onClick={() => navigate(paths.templateEditor(tpl.id))}
              >
                {t('templates.editor.open')}
              </Button>
            ) : null}
            {canUpdate ? (
              <Button icon={<IconEdit size={16} />} onClick={() => setDialog('edit')}>
                {t('common.edit')}
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
            {archived && can(...PERMISSIONS.templates.delete) ? (
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
      {tpl.description ? (
        <p className="cf-text-secondary" style={{ marginBottom: 'var(--cf-space-4)' }}>
          {tpl.description}
        </p>
      ) : null}
      {archived ? (
        <div style={{ marginBottom: 'var(--cf-space-4)' }}>
          <Alert tone="warning">{t('templates.archivedNotice')}</Alert>
        </div>
      ) : null}
      <Tabs items={tabs} value={tab} onChange={setTab} label={tpl.name} />
      {tab === 'versions' ? (
        <VersionsTab template={tpl} onChanged={detail.reload} onPreview={openPreview} />
      ) : null}
      {tab === 'preview' ? <PreviewTab template={tpl} /> : null}

      {dialog === 'edit' ? (
        <TemplateEditForm
          template={tpl}
          onClose={close}
          onSaved={() => {
            toast.success(t('templates.updated'));
            close();
            detail.reload();
          }}
        />
      ) : null}
      <ConfirmDialog
        open={dialog === 'archive'}
        title={t('templates.archive')}
        message={t('templates.archiveConfirm', { name: tpl.name })}
        confirmLabel={t('templates.archive')}
        onCancel={close}
        onConfirm={() => setStatus('archived')}
      />
      <ConfirmDialog
        open={dialog === 'restore'}
        title={t('templates.restore')}
        message={t('templates.restoreConfirm', { name: tpl.name })}
        confirmLabel={t('templates.restore')}
        onCancel={close}
        onConfirm={() => setStatus('active')}
      />
      <ConfirmDialog
        open={dialog === 'delete'}
        title={t('templates.delete')}
        message={t('templates.deleteConfirm', { name: tpl.name })}
        confirmLabel={t('common.delete')}
        danger
        errorOverrides={{ [ERROR_CODES.CONFLICT]: 'templates.deleteNotArchived' }}
        onCancel={close}
        onConfirm={async () => {
          await templatesApi.remove(tpl.id);
          toast.success(t('templates.deleted'));
          navigate(paths.templates, { replace: true });
        }}
      />
    </div>
  );
}

interface TemplateEditFormProps {
  template: TemplateDetail;
  onClose: () => void;
  onSaved: () => void;
}

function TemplateEditForm(props: TemplateEditFormProps) {
  const meta = useResource(templatesMeta);
  return (
    <ResourceGate
      resource={meta}
      modal={{ title: t('templates.form.editTitle'), onClose: props.onClose }}
    >
      {(data) => <EditForm {...props} meta={data} />}
    </ResourceGate>
  );
}

function EditForm({
  template,
  onClose,
  onSaved,
  meta,
}: TemplateEditFormProps & { meta: TemplatesMeta }) {
  const [name, setName] = useState(template.name);
  const [description, setDescription] = useState(template.description);
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});

  const action = useAction(async () => {
    const body = {
      name: changed(name.trim(), template.name),
      description: changed(description.trim(), template.description),
    };
    if (isEmptyPatch(body)) {
      onClose();
      return;
    }
    await templatesApi.update(template.id, body);
    onSaved();
  });

  const submit = async () => {
    const next = {
      name:
        validateField(name, rules.required, rules.maxLength(meta.limits.max_name_length)) ??
        undefined,
      description:
        validateField(description, rules.maxLength(meta.limits.max_description_length)) ??
        undefined,
    };
    setErrors(next);
    if (next.name || next.description) return;
    await action.run();
  };

  return (
    <FormModal
      id="template-edit-form"
      title={t('templates.form.editTitle')}
      submitLabel={t('common.save')}
      busy={action.busy}
      error={action.error}
      errorOverrides={{ [ERROR_CODES.CONFLICT]: 'templates.exists' }}
      onClose={onClose}
      onSubmit={submit}
    >
      <FormField label={t('common.name')} htmlFor="template-edit-name" required error={errors.name}>
        <Input
          id="template-edit-name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          invalid={Boolean(errors.name)}
        />
      </FormField>
      <FormField
        label={t('common.description')}
        htmlFor="template-edit-description"
        error={errors.description}
      >
        <Textarea
          id="template-edit-description"
          rows={3}
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
      </FormField>
    </FormModal>
  );
}
