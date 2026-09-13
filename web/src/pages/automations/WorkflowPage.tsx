import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  automationsApi,
  automationsMeta,
  workflowStatusInfo,
  type AutomationsMeta,
  type Workflow,
} from '@/api/automations';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { useQuery, type QueryState } from '@/hooks/useQuery';
import { useTabParam } from '@/hooks/useTabParam';
import {
  Alert,
  Button,
  Card,
  DescriptionList,
  ErrorState,
  FormField,
  Modal,
  PageHeader,
  Skeleton,
  Tabs,
  Textarea,
  useToast,
} from '@/design/components';
import { IconArchive, IconPause, IconPlay, IconRefresh, IconTrash } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { t, tEnum } from '@/i18n';
import { paths } from '@/paths';
import { FormModal } from '@/pages/shared/FormModal';
import { MissingPermission } from '@/pages/shared/MissingPermission';
import { ResourceGate } from '@/pages/shared/ResourceGate';
import { ActionError, AUTOMATION_ERRORS } from './automationErrors';
import { describePause } from './automationReason';
import { WorkflowStatusBadge } from './automationStatus';
import { RunsCard } from './RunsCard';
import { WorkflowEditor } from './WorkflowEditor';
import { buildRequest, draftFromWorkflow, emptyDraft, type DraftErrors } from './workflowDraft';
import { loadWorkflowOptions, type WorkflowOptions } from './workflowOptions';

interface EditorContext {
  meta: AutomationsMeta;
  options: WorkflowOptions;
}

/** Catalogo y opciones del editor, segun lo que el rol puede leer. */
function useEditorContext(): QueryState<EditorContext> {
  const { can } = useAccess();
  const access = {
    templates: can(...PERMISSIONS.templates.read),
    lists: can(...PERMISSIONS.contactLists.read),
    campaigns: can(...PERMISSIONS.campaigns.read),
  };
  return useQuery(async () => {
    const meta = await automationsMeta.get();
    const options = await loadWorkflowOptions(access, meta.template_kinds.send_email);
    return { meta, options };
  }, [access.templates, access.lists, access.campaigns]);
}

/** /marketing/automations/new y /marketing/automations/:id. */
export default function WorkflowPage() {
  const { id } = useParams();
  return id ? <WorkflowDetail id={id} /> : <NewWorkflow />;
}

function NewWorkflow() {
  const { can } = useAccess();
  const context = useEditorContext();
  const back = { to: paths.automations, label: t('nav.automations') };
  if (!can(...PERMISSIONS.automationWorkflows.create)) {
    return <MissingPermission title={t('automations.new')} />;
  }
  return (
    <div>
      <PageHeader title={t('automations.new')} description={t('automations.newHint')} back={back} />
      <ResourceGate resource={context}>{(ctx) => <CreateForm context={ctx} />}</ResourceGate>
    </div>
  );
}

function CreateForm({ context }: { context: EditorContext }) {
  const navigate = useNavigate();
  const toast = useToast();
  const [draft, setDraft] = useState(() => emptyDraft(context.meta));
  const [errors, setErrors] = useState<DraftErrors>({});
  const action = useAction(async () => {
    const built = buildRequest(draft, context.meta);
    setErrors(built.errors);
    if (!built.request) return;
    const { data } = await automationsApi.createWorkflow(built.request);
    toast.success(t('automations.created'));
    navigate(paths.automation(data.id), { replace: true });
  });
  return (
    <Card>
      <form
        className="cf-stack"
        noValidate
        onSubmit={(e) => {
          e.preventDefault();
          void action.run();
        }}
      >
        <WorkflowEditor
          meta={context.meta}
          options={context.options}
          draft={draft}
          onChange={setDraft}
          errors={errors}
          readOnly={false}
        />
        <ActionError error={action.error} />
        <div className="cf-form__actions">
          <Button onClick={() => navigate(paths.automations)} disabled={action.busy}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" variant="primary" loading={action.busy}>
            {t('automations.createDraft')}
          </Button>
        </div>
      </form>
    </Card>
  );
}

type TabId = 'definition' | 'runs';
type Dialog = 'activate' | 'pause' | 'archive' | 'delete' | null;

function WorkflowDetail({ id }: { id: string }) {
  const navigate = useNavigate();
  const toast = useToast();
  const { can } = useAccess();
  const [dialog, setDialog] = useState<Dialog>(null);
  const workflow = useQuery(async () => (await automationsApi.getWorkflow(id)).data, [id]);
  const context = useEditorContext();
  const canRuns = can(...PERMISSIONS.automationRuns.read);
  const tabs: { id: TabId; label: string }[] = [
    { id: 'definition', label: t('automations.tab.definition') },
    ...(canRuns ? [{ id: 'runs' as const, label: t('automations.tab.runs') }] : []),
  ];
  const [tab, setTab] = useTabParam(
    tabs.map((item) => item.id),
    'definition',
  );
  const back = { to: paths.automations, label: t('nav.automations') };

  if (workflow.error || context.error) {
    const failed = workflow.error ?? context.error;
    return (
      <div>
        <PageHeader title={t('automations.title')} back={back} />
        <Card>
          <ErrorState
            error={failed}
            title={workflow.error ? t('automations.notFound') : undefined}
            onRetry={() => {
              workflow.reload();
              context.reload();
            }}
          />
        </Card>
      </div>
    );
  }
  if (!workflow.data || !context.data) {
    return (
      <Card>
        <Skeleton lines={8} />
      </Card>
    );
  }

  const w = workflow.data;
  const { meta, options } = context.data;
  const info = workflowStatusInfo(meta, w.status);
  const canTransition = can(...PERMISSIONS.automationWorkflows.activate);
  const canUpdate = can(...PERMISSIONS.automationWorkflows.update);
  const close = () => setDialog(null);
  const applied = (next: Workflow, message: string) => {
    workflow.setData(next);
    toast.success(message);
    close();
  };

  return (
    <div>
      <PageHeader
        title={w.name}
        description={
          <span className="cf-inline">
            <WorkflowStatusBadge status={w.status} />
            <span>{tEnum('automations.trigger', w.trigger.type)}</span>
          </span>
        }
        back={back}
        actions={
          <>
            <Button variant="ghost" icon={<IconRefresh size={16} />} onClick={workflow.reload}>
              {t('common.refresh')}
            </Button>
            {canTransition && info?.can_activate ? (
              <Button
                variant="primary"
                icon={<IconPlay size={16} />}
                onClick={() => setDialog('activate')}
              >
                {t('automations.activate.action')}
              </Button>
            ) : null}
            {canTransition && info?.can_pause ? (
              <Button icon={<IconPause size={16} />} onClick={() => setDialog('pause')}>
                {t('automations.pause.action')}
              </Button>
            ) : null}
            {canTransition && info?.can_archive ? (
              <Button icon={<IconArchive size={16} />} onClick={() => setDialog('archive')}>
                {t('automations.archive.action')}
              </Button>
            ) : null}
            {can(...PERMISSIONS.automationWorkflows.delete) && info?.deletable ? (
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
      {tabs.length > 1 ? (
        <Tabs items={tabs} value={tab} onChange={setTab} label={t('automations.title')} />
      ) : null}
      {tab === 'runs' ? (
        <RunsCard workflow={w} meta={meta} />
      ) : (
        <DefinitionTab
          key={w.updated_at}
          workflow={w}
          meta={meta}
          options={options}
          canUpdate={canUpdate}
          editable={canUpdate && Boolean(info?.editable)}
          onSaved={(next) => applied(next, t('automations.updated'))}
        />
      )}

      {dialog === 'activate' ? (
        <ActionDialog
          title={t('automations.activate.action')}
          message={t('automations.activate.confirm', { name: w.name })}
          confirmLabel={t('automations.activate.action')}
          onCancel={close}
          onConfirm={async () =>
            applied((await automationsApi.activate(w.id)).data, t('automations.activate.done'))
          }
        />
      ) : null}
      {dialog === 'pause' ? (
        <PauseForm
          workflow={w}
          maxLength={meta.limits.max_pause_reason_length}
          onClose={close}
          onDone={(next) => applied(next, t('automations.pause.done'))}
        />
      ) : null}
      {dialog === 'archive' ? (
        <ActionDialog
          title={t('automations.archive.action')}
          message={t('automations.archive.confirm', { name: w.name })}
          confirmLabel={t('automations.archive.action')}
          danger
          onCancel={close}
          onConfirm={async () =>
            applied((await automationsApi.archive(w.id)).data, t('automations.archive.done'))
          }
        />
      ) : null}
      {dialog === 'delete' ? (
        <ActionDialog
          title={t('automations.delete')}
          message={t('automations.deleteConfirm', { name: w.name })}
          confirmLabel={t('common.delete')}
          danger
          onCancel={close}
          onConfirm={async () => {
            await automationsApi.deleteWorkflow(w.id);
            toast.success(t('automations.deleted'));
            navigate(paths.automations, { replace: true });
          }}
        />
      ) : null}
    </div>
  );
}

function DefinitionTab({
  workflow: w,
  meta,
  options,
  canUpdate,
  editable,
  onSaved,
}: {
  workflow: Workflow;
  meta: AutomationsMeta;
  options: WorkflowOptions;
  canUpdate: boolean;
  editable: boolean;
  onSaved: (next: Workflow) => void;
}) {
  const [draft, setDraft] = useState(() => draftFromWorkflow(w, meta));
  const [errors, setErrors] = useState<DraftErrors>({});
  const save = useAction(async () => {
    const built = buildRequest(draft, meta);
    setErrors(built.errors);
    if (!built.request) return;
    onSaved((await automationsApi.updateWorkflow(w.id, built.request)).data);
  });
  const pause = describePause(w.pause_reason, meta.pause_reason_manual);

  return (
    <div className="cf-stack">
      {w.status === 'paused' && pause ? (
        <Alert tone="warning" title={t('automations.pause.reasonTitle')}>
          <span>{pause.text}</span>
          {pause.detail ? <span className="cf-text-sm cf-break">{pause.detail}</span> : null}
        </Alert>
      ) : null}
      {canUpdate && !editable ? (
        <Alert tone="info">{t('automations.editor.lockedHint')}</Alert>
      ) : null}
      <Card title={t('automations.detail.summary')}>
        <DescriptionList
          items={[
            { label: t('common.status'), value: <WorkflowStatusBadge status={w.status} /> },
            {
              label: t('automations.column.activatedAt'),
              value: w.activated_at ? formatDateTime(w.activated_at) : t('common.dash'),
            },
            { label: t('common.createdAt'), value: formatDateTime(w.created_at) },
            { label: t('common.updatedAt'), value: formatDateTime(w.updated_at) },
            { label: t('common.id'), value: <span className="cf-mono">{w.id}</span> },
          ]}
        />
      </Card>
      <Card title={t('automations.detail.definition')}>
        <form
          className="cf-stack"
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            if (editable) void save.run();
          }}
        >
          <WorkflowEditor
            meta={meta}
            options={options}
            draft={draft}
            onChange={setDraft}
            errors={errors}
            readOnly={!editable}
          />
          <ActionError error={save.error} />
          {editable ? (
            <div className="cf-form__actions">
              <Button
                onClick={() => {
                  setDraft(draftFromWorkflow(w, meta));
                  setErrors({});
                  save.clearError();
                }}
                disabled={save.busy}
              >
                {t('automations.editor.discard')}
              </Button>
              <Button type="submit" variant="primary" loading={save.busy}>
                {t('common.save')}
              </Button>
            </div>
          ) : null}
        </form>
      </Card>
    </div>
  );
}

function PauseForm({
  workflow,
  maxLength,
  onClose,
  onDone,
}: {
  workflow: Workflow;
  maxLength: number;
  onClose: () => void;
  onDone: (next: Workflow) => void;
}) {
  const [reason, setReason] = useState('');
  const action = useAction(async () => {
    onDone((await automationsApi.pause(workflow.id, reason.trim())).data);
  });
  return (
    <FormModal
      id="workflow-pause"
      title={t('automations.pause.action')}
      submitLabel={t('automations.pause.action')}
      busy={action.busy}
      error={action.error}
      errorOverrides={AUTOMATION_ERRORS}
      onClose={onClose}
      onSubmit={async () => {
        await action.run();
      }}
    >
      <p className="cf-text-secondary">{t('automations.pause.confirm', { name: workflow.name })}</p>
      <FormField
        label={t('automations.pause.reason')}
        htmlFor="workflow-pause-reason"
        hint={t('automations.pause.reasonHint')}
      >
        <Textarea
          id="workflow-pause-reason"
          rows={3}
          maxLength={maxLength}
          value={reason}
          onChange={(e) => setReason(e.target.value)}
        />
      </FormField>
    </FormModal>
  );
}

/**
 * Confirmacion de una accion con su error debajo. A diferencia de ConfirmDialog, muestra
 * tambien el mensaje del servicio cuando dice que paso falla (al activar).
 */
function ActionDialog({
  title,
  message,
  confirmLabel,
  danger = false,
  onConfirm,
  onCancel,
}: {
  title: string;
  message: string;
  confirmLabel: string;
  danger?: boolean;
  onConfirm: () => Promise<void>;
  onCancel: () => void;
}) {
  const action = useAction(onConfirm);
  return (
    <Modal
      open
      title={title}
      onClose={() => {
        if (!action.busy) onCancel();
      }}
      footer={
        <>
          <Button onClick={onCancel} disabled={action.busy}>
            {t('common.cancel')}
          </Button>
          <Button
            variant={danger ? 'danger' : 'primary'}
            loading={action.busy}
            onClick={() => void action.run()}
          >
            {confirmLabel}
          </Button>
        </>
      }
    >
      <p className="cf-modal__message">{message}</p>
      <ActionError error={action.error} />
    </Modal>
  );
}
