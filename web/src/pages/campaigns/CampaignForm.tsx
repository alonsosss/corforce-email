import { useState } from 'react';
import {
  campaignsApi,
  type Audience,
  type Campaign,
  type CreateCampaignRequest,
  type UpdateCampaignRequest,
} from '@/api/campaigns';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import { FormField, Input, Select, Textarea } from '@/design/components';
import { changed, isEmptyPatch } from '@/lib/patch';
import { rules, validateField } from '@/lib/validate';
import { t } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { ResourceGate } from '@/pages/shared/ResourceGate';
import { AudiencePicker } from './AudiencePicker';
import { loadCampaignOptions, type CampaignOptions } from './campaignOptions';
import { campaignRules } from './campaignRules';

export interface CampaignFormProps {
  /** null en el alta. */
  campaign: Campaign | null;
  onClose: () => void;
  onSaved: (campaign: Campaign) => void;
}

const EMPTY_AUDIENCE: Audience = { list_ids: [], segment_ids: [], exclude_segment_ids: [] };

function sameAudience(a: Audience, b: Audience): boolean {
  const key = (x: Audience) =>
    JSON.stringify([
      [...x.list_ids].sort(),
      [...x.segment_ids].sort(),
      [...x.exclude_segment_ids].sort(),
    ]);
  return key(a) === key(b);
}

export function CampaignForm(props: CampaignFormProps) {
  const { can } = useAccess();
  const access = {
    templates: can(...PERMISSIONS.templates.read),
    lists: can(...PERMISSIONS.contactLists.read),
    segments: can(...PERMISSIONS.segments.read),
  };
  const options = useQuery(
    () => loadCampaignOptions(access),
    [access.templates, access.lists, access.segments],
  );
  const title = props.campaign ? t('campaigns.form.editTitle') : t('campaigns.form.createTitle');
  return (
    <ResourceGate resource={options} modal={{ title, onClose: props.onClose }}>
      {(data) => <Form {...props} options={data} title={title} />}
    </ResourceGate>
  );
}

function Form({
  campaign,
  options,
  title,
  onClose,
  onSaved,
}: CampaignFormProps & { options: CampaignOptions; title: string }) {
  const [name, setName] = useState(campaign?.name ?? '');
  const [description, setDescription] = useState(campaign?.description ?? '');
  const [templateId, setTemplateId] = useState(campaign?.template_id ?? '');
  const [fromEmail, setFromEmail] = useState(campaign?.from_email ?? '');
  const [fromName, setFromName] = useState(campaign?.from_name ?? '');
  const [replyTo, setReplyTo] = useState(campaign?.reply_to ?? '');
  const [audience, setAudience] = useState<Audience>(campaign?.audience ?? EMPTY_AUDIENCE);
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});
  const locked = campaign ? campaignRules.contentLocked(campaign.status) : false;

  const action = useAction(async (body: CreateCampaignRequest | UpdateCampaignRequest | null) => {
    if (!campaign) {
      onSaved((await campaignsApi.create(body as CreateCampaignRequest)).data);
      return;
    }
    if (!body) {
      onClose();
      return;
    }
    onSaved((await campaignsApi.update(campaign.id, body)).data);
  });

  const submit = async () => {
    const next = {
      name: validateField(name, rules.required) ?? undefined,
      templateId: templateId.trim() ? undefined : t('validation.required'),
      fromEmail: validateField(fromEmail, rules.required, rules.email) ?? undefined,
      replyTo: replyTo.trim() ? (validateField(replyTo, rules.email) ?? undefined) : undefined,
      audience:
        audience.list_ids.length + audience.segment_ids.length === 0
          ? t('campaigns.audience.required')
          : undefined,
    };
    setErrors(next);
    if (Object.values(next).some(Boolean)) return;

    const values: CreateCampaignRequest = {
      name: name.trim(),
      description: description.trim(),
      template_id: templateId.trim(),
      from_email: fromEmail.trim(),
      from_name: fromName.trim(),
      reply_to: replyTo.trim(),
      audience,
    };
    if (!campaign) {
      await action.run(values);
      return;
    }
    const body: UpdateCampaignRequest = {
      name: changed(values.name, campaign.name),
      description: changed(values.description, campaign.description),
      from_email: changed(values.from_email, campaign.from_email),
      from_name: changed(values.from_name, campaign.from_name),
      reply_to: changed(values.reply_to, campaign.reply_to),
      template_id: locked ? undefined : changed(values.template_id, campaign.template_id),
      audience: locked || sameAudience(audience, campaign.audience) ? undefined : audience,
    };
    await action.run(isEmptyPatch(body) ? null : body);
  };

  const templates = options.templates;
  const templateOptions = (templates ?? []).map((tpl) => ({
    value: tpl.id,
    label:
      tpl.current_version > 0
        ? t('campaigns.form.templateOption', { name: tpl.name, n: tpl.current_version })
        : t('campaigns.form.templateUnpublished', { name: tpl.name }),
  }));
  if (templateId && templates && !templates.some((tpl) => tpl.id === templateId)) {
    templateOptions.push({
      value: templateId,
      label: t('campaigns.form.templateOther', { id: templateId }),
    });
  }

  return (
    <FormModal
      id="campaign-form"
      title={title}
      submitLabel={campaign ? t('common.save') : t('campaigns.form.createDraft')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={submit}
      size="lg"
    >
      <div className="cf-form__row">
        <FormField label={t('common.name')} htmlFor="campaign-name" required error={errors.name}>
          <Input
            id="campaign-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            invalid={Boolean(errors.name)}
          />
        </FormField>
        <FormField
          label={t('campaigns.column.template')}
          htmlFor="campaign-template"
          required
          error={errors.templateId}
          hint={locked ? t('campaigns.form.lockedHint') : t('campaigns.form.templateHint')}
        >
          {templates ? (
            <Select
              id="campaign-template"
              placeholder={t('common.select')}
              options={templateOptions}
              value={templateId}
              onChange={(e) => setTemplateId(e.target.value)}
              disabled={locked}
              invalid={Boolean(errors.templateId)}
            />
          ) : (
            <Input
              id="campaign-template"
              className="cf-mono"
              placeholder={t('campaigns.form.templateId')}
              value={templateId}
              onChange={(e) => setTemplateId(e.target.value)}
              readOnly={locked}
              invalid={Boolean(errors.templateId)}
            />
          )}
        </FormField>
      </div>
      <FormField label={t('common.description')} htmlFor="campaign-description">
        <Textarea
          id="campaign-description"
          rows={2}
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
      </FormField>
      <div className="cf-form__section">{t('campaigns.form.sender')}</div>
      <div className="cf-form__row">
        <FormField
          label={t('campaigns.form.fromEmail')}
          htmlFor="campaign-from-email"
          required
          error={errors.fromEmail}
          hint={t('campaigns.form.fromEmailHint')}
        >
          <Input
            id="campaign-from-email"
            type="email"
            className="cf-mono"
            value={fromEmail}
            onChange={(e) => setFromEmail(e.target.value)}
            invalid={Boolean(errors.fromEmail)}
          />
        </FormField>
        <FormField label={t('campaigns.form.fromName')} htmlFor="campaign-from-name">
          <Input
            id="campaign-from-name"
            value={fromName}
            onChange={(e) => setFromName(e.target.value)}
          />
        </FormField>
        <FormField
          label={t('campaigns.form.replyTo')}
          htmlFor="campaign-reply-to"
          error={errors.replyTo}
        >
          <Input
            id="campaign-reply-to"
            type="email"
            className="cf-mono"
            value={replyTo}
            onChange={(e) => setReplyTo(e.target.value)}
            invalid={Boolean(errors.replyTo)}
          />
        </FormField>
      </div>
      <div className="cf-form__section">{t('campaigns.audience.title')}</div>
      <span className="cf-text-sm cf-text-secondary">
        {locked ? t('campaigns.form.lockedHint') : t('campaigns.audience.hint')}
      </span>
      <AudiencePicker
        value={audience}
        onChange={setAudience}
        options={options}
        disabled={locked}
        error={errors.audience}
      />
    </FormModal>
  );
}
