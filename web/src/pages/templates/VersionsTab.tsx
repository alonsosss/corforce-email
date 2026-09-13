import { useState } from 'react';
import {
  templatesApi,
  templatesMeta,
  type TemplateContent,
  type TemplateDetail,
  type TemplatesMeta,
  type VersionSummary,
} from '@/api/templates';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import { useResource } from '@/hooks/useResource';
import {
  Badge,
  Button,
  Card,
  ConfirmDialog,
  DataTable,
  DescriptionList,
  ErrorState,
  HtmlPreviewFrame,
  Modal,
  Skeleton,
  useToast,
  type Column,
} from '@/design/components';
import { IconPlus } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { t, tEnum } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { RowActions } from '@/pages/shared/RowActions';
import { ContentFields } from './ContentFields';
import {
  contentFromDraft,
  contentFromVersion,
  type ContentDraft,
  type ContentErrors,
} from './content';
import { versionStatusTone } from './templateStatus';

export interface VersionsTabProps {
  template: TemplateDetail;
  onChanged: () => void;
  onPreview: (version: number) => void;
}

export function VersionsTab({ template, onChanged, onPreview }: VersionsTabProps) {
  const toast = useToast();
  const { can } = useAccess();
  const [viewing, setViewing] = useState<number | null>(null);
  const [publishing, setPublishing] = useState<VersionSummary | null>(null);
  const [creating, setCreating] = useState(false);

  const archived = template.status === 'archived';
  const canCreate = can(...PERMISSIONS.templates.create) && !archived;
  const canPublish = can(...PERMISSIONS.templates.publish) && !archived;
  const canRender = can(...PERMISSIONS.templates.render);
  const versions = [...template.versions].sort((a, b) => b.version - a.version);
  const latest = versions[0]?.version ?? null;

  const columns: Column<VersionSummary>[] = [
    {
      key: 'version',
      header: t('templates.versions.column.version'),
      render: (v) => (
        <span className="cf-inline">
          <strong>{t('templates.versionLabel', { n: v.version })}</strong>
          {v.version === template.current_version ? (
            <Badge tone="accent">{t('templates.versions.current')}</Badge>
          ) : null}
        </span>
      ),
    },
    {
      key: 'status',
      header: t('common.status'),
      render: (v) => (
        <Badge tone={versionStatusTone(v.status)}>
          {tEnum('templates.versionStatus', v.status)}
        </Badge>
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
          {canRender ? (
            <Button size="sm" variant="ghost" onClick={() => onPreview(v.version)}>
              {t('templates.tab.preview')}
            </Button>
          ) : null}
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
      description={t('templates.versions.description')}
      actions={
        canCreate ? (
          <Button variant="primary" icon={<IconPlus size={16} />} onClick={() => setCreating(true)}>
            {t('templates.versions.new')}
          </Button>
        ) : null
      }
    >
      <DataTable
        columns={columns}
        rows={versions}
        rowKey={(v) => v.id}
        empty={{ title: t('templates.versions.empty') }}
      />
      {viewing !== null ? (
        <VersionViewer
          templateId={template.id}
          version={viewing}
          onClose={() => setViewing(null)}
        />
      ) : null}
      {creating ? (
        <NewVersion
          templateId={template.id}
          baseVersion={latest}
          onClose={() => setCreating(false)}
          onCreated={(version) => {
            toast.success(t('templates.versions.created', { n: version }));
            setCreating(false);
            onChanged();
          }}
        />
      ) : null}
      <ConfirmDialog
        open={publishing !== null}
        title={t('templates.versions.publish')}
        message={
          publishing
            ? template.current_version > 0
              ? t('templates.versions.publishReplace', {
                  n: publishing.version,
                  current: template.current_version,
                })
              : t('templates.versions.publishFirst', { n: publishing.version })
            : ''
        }
        confirmLabel={t('templates.versions.publish')}
        onCancel={() => setPublishing(null)}
        onConfirm={async () => {
          if (!publishing) return;
          await templatesApi.publish(template.id, publishing.version);
          toast.success(t('templates.versions.published', { n: publishing.version }));
          setPublishing(null);
          onChanged();
        }}
      />
    </Card>
  );
}

function VersionViewer({
  templateId,
  version,
  onClose,
}: {
  templateId: string;
  version: number;
  onClose: () => void;
}) {
  const content = useQuery(
    async () => (await templatesApi.getVersion(templateId, version)).data,
    [templateId, version],
  );
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
              { label: t('templates.content.subject'), value: v.subject },
              {
                label: t('common.status'),
                value: (
                  <Badge tone={versionStatusTone(v.status)}>
                    {tEnum('templates.versionStatus', v.status)}
                  </Badge>
                ),
              },
              {
                label: t('templates.versions.column.publishedAt'),
                value: formatDateTime(v.published_at),
              },
              {
                label: t('templates.variables.title'),
                value: v.variables?.length ? (
                  <span className="cf-inline-list">
                    {v.variables.map((variable) => (
                      <Badge key={variable.name} tone={variable.required ? 'accent' : 'neutral'}>
                        <span className="cf-mono">
                          {variable.name}: {tEnum('templates.variableType', variable.type)}
                        </span>
                      </Badge>
                    ))}
                  </span>
                ) : (
                  t('common.none')
                ),
              },
            ]}
          />
          <div className="cf-field">
            <span className="cf-field__label">{t('templates.content.sourcePreview')}</span>
            <HtmlPreviewFrame
              html={v.html}
              title={t('templates.content.sourcePreview')}
              height={320}
            />
          </div>
          <div className="cf-field">
            <span className="cf-field__label">{t('templates.content.html')}</span>
            <pre className="cf-pre cf-pre--tall">{v.html}</pre>
          </div>
          {v.text ? (
            <div className="cf-field">
              <span className="cf-field__label">{t('templates.content.text')}</span>
              <pre className="cf-pre">{v.text}</pre>
            </div>
          ) : null}
        </div>
      )}
    </Modal>
  );
}

function NewVersion({
  templateId,
  baseVersion,
  onClose,
  onCreated,
}: {
  templateId: string;
  baseVersion: number | null;
  onClose: () => void;
  onCreated: (version: number) => void;
}) {
  const base = useQuery(
    async () =>
      baseVersion ? (await templatesApi.getVersion(templateId, baseVersion)).data : null,
    [templateId, baseVersion],
  );
  const meta = useResource(templatesMeta);
  const failed = base.error ?? meta.error;
  if (base.loading || failed || !meta.data) {
    return (
      <Modal open title={t('templates.versions.new')} onClose={onClose}>
        {failed ? (
          <ErrorState
            error={failed}
            onRetry={() => {
              base.reload();
              meta.reload();
            }}
          />
        ) : (
          <Skeleton lines={6} />
        )}
      </Modal>
    );
  }
  return (
    <NewVersionForm
      templateId={templateId}
      initial={contentFromVersion(base.data)}
      meta={meta.data}
      onClose={onClose}
      onCreated={onCreated}
    />
  );
}

function NewVersionForm({
  templateId,
  initial,
  meta,
  onClose,
  onCreated,
}: {
  templateId: string;
  initial: ContentDraft;
  meta: TemplatesMeta;
  onClose: () => void;
  onCreated: (version: number) => void;
}) {
  const [content, setContent] = useState<ContentDraft>(initial);
  const [errors, setErrors] = useState<ContentErrors>({});

  const action = useAction(async (input: TemplateContent) => {
    const { data } = await templatesApi.createVersion(templateId, input);
    onCreated(data.version);
  });

  const submit = async () => {
    const parsed = contentFromDraft(content, meta);
    setErrors(parsed.errors);
    if (!parsed.content) return;
    await action.run(parsed.content);
  };

  return (
    <FormModal
      id="template-version-form"
      title={t('templates.versions.new')}
      submitLabel={t('templates.versions.saveDraft')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={submit}
      size="lg"
    >
      <span className="cf-text-sm cf-text-secondary">{t('templates.versions.newHint')}</span>
      <ContentFields
        idPrefix="template-version"
        value={content}
        onChange={setContent}
        errors={errors}
        meta={meta}
      />
    </FormModal>
  );
}
