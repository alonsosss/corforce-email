import { useState } from 'react';
import { Link } from 'react-router-dom';
import {
  automationsApi,
  automationsMeta,
  type AutomationsMeta,
  type DoiDelivery,
  type DoiSettings,
  type DoiStatus,
} from '@/api/automations';
import type { Template } from '@/api/templates';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import { useResource } from '@/hooks/useResource';
import {
  Alert,
  Button,
  Card,
  Checkbox,
  DataTable,
  FormField,
  Input,
  Select,
  useToast,
  type Column,
} from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { rules, validateField } from '@/lib/validate';
import { getLocale, t, tEnum } from '@/i18n';
import { paths } from '@/paths';
import { ResourceGate } from '@/pages/shared/ResourceGate';
import { ActionError } from './automationErrors';
import { describeDoiReason } from './automationReason';
import { DoiStatusBadge } from './automationStatus';
import { loadActiveTemplates } from './workflowOptions';

/** Ajustes del correo del doble opt-in y el historial de sus envios. */
export function DoubleOptInTab() {
  const { can } = useAccess();
  const canTemplates = can(...PERMISSIONS.templates.read);
  const context = useQuery(async () => {
    const meta = await automationsMeta.get();
    const [settings, templates] = await Promise.all([
      automationsApi.getDoiSettings().then((r) => r.data),
      canTemplates ? loadActiveTemplates(meta.template_kinds.double_opt_in) : Promise.resolve(null),
    ]);
    return { meta, settings, templates };
  }, [canTemplates]);

  return (
    <div className="cf-stack">
      <ResourceGate resource={context}>
        {(ctx) => (
          <SettingsCard
            key={ctx.settings.updated_at ?? 'sin-ajustes'}
            meta={ctx.meta}
            settings={ctx.settings}
            templates={ctx.templates}
            canUpdate={can(...PERMISSIONS.automationSettings.update)}
            onSaved={(settings) => context.setData((cur) => (cur ? { ...cur, settings } : cur))}
          />
        )}
      </ResourceGate>
      <DeliveriesCard />
    </div>
  );
}

function SettingsCard({
  meta,
  settings,
  templates,
  canUpdate,
  onSaved,
}: {
  meta: AutomationsMeta;
  settings: DoiSettings;
  templates: Template[] | null;
  canUpdate: boolean;
  onSaved: (settings: DoiSettings) => void;
}) {
  const toast = useToast();
  const [enabled, setEnabled] = useState(settings.enabled);
  const [templateId, setTemplateId] = useState(settings.template_id ?? '');
  const [fromEmail, setFromEmail] = useState(settings.from_email);
  const [fromName, setFromName] = useState(settings.from_name);
  const [replyTo, setReplyTo] = useState(settings.reply_to);
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});

  const save = useAction(async () => {
    const template = templateId.trim();
    const next = {
      template:
        enabled && !template
          ? t('automations.doi.templateRequired')
          : template
            ? (rules.uuid(template) ?? undefined)
            : undefined,
      fromEmail: enabled
        ? (validateField(fromEmail, rules.required, rules.email) ?? undefined)
        : fromEmail.trim()
          ? (validateField(fromEmail, rules.email) ?? undefined)
          : undefined,
      fromName: validateField(fromName, rules.maxLength(meta.limits.max_name_length)) ?? undefined,
      replyTo: replyTo.trim() ? (validateField(replyTo, rules.email) ?? undefined) : undefined,
    };
    setErrors(next);
    if (Object.values(next).some(Boolean)) return;
    const { data } = await automationsApi.putDoiSettings({
      enabled,
      template_id: template || null,
      from_email: fromEmail.trim(),
      from_name: fromName.trim(),
      reply_to: replyTo.trim(),
    });
    toast.success(t('automations.doi.saved'));
    onSaved(data);
  });

  const options = (templates ?? []).map((tpl) => ({
    value: tpl.id,
    label:
      tpl.current_version > 0
        ? t('automations.step.templateOption', { name: tpl.name, n: tpl.current_version })
        : t('automations.step.templateUnpublished', { name: tpl.name }),
  }));
  if (templateId && !options.some((o) => o.value === templateId)) {
    options.push({
      value: templateId,
      label: t('automations.step.templateOther', { id: templateId }),
    });
  }
  const readOnly = !canUpdate;

  return (
    <Card title={t('automations.doi.title')} description={t('automations.doi.description')}>
      <form
        className="cf-form"
        noValidate
        onSubmit={(e) => {
          e.preventDefault();
          if (canUpdate) void save.run();
        }}
      >
        <Alert tone="info">
          {t('automations.doi.limits', {
            perDay: meta.doi_limits.per_day,
            per30: meta.doi_limits.per_30_days,
          })}
        </Alert>
        <Checkbox
          label={t('automations.doi.enabled')}
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
          disabled={readOnly}
        />
        <FormField
          label={t('automations.doi.template')}
          htmlFor="doi-template"
          required={enabled}
          error={errors.template}
          hint={t('automations.doi.templateHint')}
        >
          {templates ? (
            <Select
              id="doi-template"
              placeholder={t('common.select')}
              options={options}
              value={templateId}
              onChange={(e) => setTemplateId(e.target.value)}
              disabled={readOnly}
              invalid={Boolean(errors.template)}
            />
          ) : (
            <Input
              id="doi-template"
              className="cf-mono"
              placeholder={t('automations.editor.idPlaceholder')}
              value={templateId}
              onChange={(e) => setTemplateId(e.target.value)}
              disabled={readOnly}
              invalid={Boolean(errors.template)}
              autoComplete="off"
              spellCheck={false}
            />
          )}
        </FormField>
        <div className="cf-form__row">
          <FormField
            label={t('automations.step.fromEmail')}
            htmlFor="doi-from-email"
            required={enabled}
            error={errors.fromEmail}
            hint={t('automations.step.fromEmailHint')}
          >
            <Input
              id="doi-from-email"
              type="email"
              className="cf-mono"
              value={fromEmail}
              onChange={(e) => setFromEmail(e.target.value)}
              disabled={readOnly}
              invalid={Boolean(errors.fromEmail)}
            />
          </FormField>
          <FormField
            label={t('automations.step.fromName')}
            htmlFor="doi-from-name"
            error={errors.fromName}
          >
            <Input
              id="doi-from-name"
              value={fromName}
              maxLength={meta.limits.max_name_length}
              onChange={(e) => setFromName(e.target.value)}
              disabled={readOnly}
              invalid={Boolean(errors.fromName)}
            />
          </FormField>
          <FormField
            label={t('automations.step.replyTo')}
            htmlFor="doi-reply-to"
            error={errors.replyTo}
          >
            <Input
              id="doi-reply-to"
              type="email"
              className="cf-mono"
              value={replyTo}
              onChange={(e) => setReplyTo(e.target.value)}
              disabled={readOnly}
              invalid={Boolean(errors.replyTo)}
            />
          </FormField>
        </div>
        {settings.updated_at ? (
          <span className="cf-text-sm cf-text-secondary">
            {t('automations.doi.updatedAt', { date: formatDateTime(settings.updated_at) })}
          </span>
        ) : null}
        <ActionError error={save.error} />
        {canUpdate ? (
          <div className="cf-form__actions">
            <Button type="submit" variant="primary" loading={save.busy}>
              {t('common.save')}
            </Button>
          </div>
        ) : null}
      </form>
    </Card>
  );
}

function DeliveriesCard() {
  const { can } = useAccess();
  const meta = useResource(automationsMeta);
  const pager = usePagination();
  const [status, setStatus] = useState<DoiStatus | ''>('');
  const format = new Intl.NumberFormat(getLocale());
  const canContacts = can(...PERMISSIONS.contacts.read);
  const deliveries = useQuery(
    () =>
      automationsApi.listDeliveries({
        page: pager.page,
        per_page: pager.perPage,
        status: status || undefined,
      }),
    [pager.page, pager.perPage, status],
  );

  const columns: Column<DoiDelivery>[] = [
    {
      key: 'contact',
      header: t('automations.runs.contact'),
      render: (d) =>
        canContacts ? (
          <Link to={paths.contact(d.contact_id)} className="cf-mono cf-text-sm">
            {d.contact_id}
          </Link>
        ) : (
          <span className="cf-mono cf-text-sm">{d.contact_id}</span>
        ),
    },
    {
      key: 'status',
      header: t('common.status'),
      render: (d) => <DoiStatusBadge status={d.status} />,
    },
    {
      key: 'reason',
      header: t('automations.doi.reason'),
      render: (d) => {
        const reason = describeDoiReason(d.reason);
        if (!reason) return t('common.dash');
        return (
          <div className="cf-cell-stack">
            <span className="cf-text-sm">{reason.text}</span>
            {reason.detail ? (
              <span className="cf-text-muted cf-text-sm cf-break">{reason.detail}</span>
            ) : null}
          </div>
        );
      },
    },
    {
      key: 'attempts',
      header: t('automations.runs.attempts'),
      align: 'right',
      render: (d) => format.format(d.attempts),
    },
    {
      key: 'sent',
      header: t('automations.doi.sentAt'),
      render: (d) => (d.sent_at ? formatDateTime(d.sent_at) : t('common.dash')),
    },
    { key: 'created', header: t('common.createdAt'), render: (d) => formatDateTime(d.created_at) },
  ];

  return (
    <Card
      flush
      title={t('automations.doi.deliveriesTitle')}
      description={t('automations.doi.deliveriesDescription')}
    >
      <div className="cf-toolbar">
        <div className="cf-field">
          <label className="cf-field__label" htmlFor="doi-deliveries-status">
            {t('common.status')}
          </label>
          <Select
            id="doi-deliveries-status"
            placeholder={t('common.all')}
            options={(meta.data?.doi_statuses ?? []).map((s) => ({
              value: s,
              label: tEnum('automations.doiStatus', s),
            }))}
            value={status}
            onChange={(e) => {
              setStatus(e.target.value as DoiStatus | '');
              pager.reset();
            }}
            disabled={!meta.data}
          />
        </div>
      </div>
      <DataTable
        columns={columns}
        rows={deliveries.data?.items ?? []}
        rowKey={(d) => d.id}
        loading={deliveries.loading}
        error={deliveries.error}
        onRetry={deliveries.reload}
        empty={{ title: t('automations.doi.deliveriesEmpty') }}
        pagination={{
          page: deliveries.data?.page ?? pager.page,
          perPage: pager.perPage,
          total: deliveries.data?.total ?? 0,
          totalPages: deliveries.data?.totalPages ?? 0,
          onPageChange: pager.setPage,
        }}
      />
    </Card>
  );
}
