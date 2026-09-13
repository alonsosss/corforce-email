import { useState, type FormEvent } from 'react';
import {
  mailSecurityApi,
  QUARANTINE_NOTICE_TEMPLATE,
  type QuarantineSettings,
  type QuarantineSettingsInput,
} from '@/api/mailSecurity';
import { errorMessage } from '@/api/messages';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import {
  Button,
  Card,
  Checkbox,
  ChipsInput,
  ErrorState,
  FormField,
  HtmlPreviewFrame,
  Input,
  Skeleton,
  Textarea,
  useToast,
} from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { normalizeDomainName, normalizeEmail } from '@/lib/mailAddress';
import { bytesToQuota, type QuotaAmount } from '@/lib/quota';
import { rules, validateField } from '@/lib/validate';
import { t } from '@/i18n';
import { QuotaField, readQuota } from '@/pages/shared/QuotaField';
import { isDecimal } from './policyObject';
import { noticeFieldError, type NoticeField } from './quarantineNotice';

export function QuarantineSettingsTab() {
  const settings = useQuery(async () => (await mailSecurityApi.getQuarantineSettings()).data, []);
  if (settings.error) {
    return (
      <Card title={t('security.quarantineSettings.title')}>
        <ErrorState error={settings.error} onRetry={settings.reload} />
      </Card>
    );
  }
  if (!settings.data) {
    return (
      <Card title={t('security.quarantineSettings.title')}>
        <Skeleton lines={8} />
      </Card>
    );
  }
  return (
    <QuarantineSettingsForm
      key={settings.data.updated_at}
      initial={settings.data}
      onSaved={settings.setData}
    />
  );
}

/** Variables de la plantilla del aviso, tal como las documenta mail-security. */
function NoticeTemplateVariables() {
  const code = (name: string) => <code key={name} className="cf-mono">{`{{${name}}}`}</code>;
  return (
    <div className="cf-field__hint cf-stack" style={{ gap: 'var(--cf-space-1)' }}>
      <span>
        {t('security.quarantineSettings.templateVariables')}{' '}
        <span className="cf-inline-list">{QUARANTINE_NOTICE_TEMPLATE.fields.map(code)}</span>
      </span>
      <span>
        {t('security.quarantineSettings.templateItems', {
          range: `{{range ${QUARANTINE_NOTICE_TEMPLATE.list}}}`,
          end: '{{end}}',
        })}{' '}
        <span className="cf-inline-list">{QUARANTINE_NOTICE_TEMPLATE.itemFields.map(code)}</span>
      </span>
      <span>{t('security.quarantineSettings.templateEscaping')}</span>
    </div>
  );
}

function QuarantineSettingsForm({
  initial,
  onSaved,
}: {
  initial: QuarantineSettings;
  onSaved: (next: QuarantineSettings) => void;
}) {
  const toast = useToast();
  const { can } = useAccess();
  const editable = can(...PERMISSIONS.quarantineSettings.update);
  const [maxSize, setMaxSize] = useState<QuotaAmount>(bytesToQuota(initial.max_size_bytes));
  const [maxAge, setMaxAge] = useState(String(initial.max_age_days));
  const [retention, setRetention] = useState(String(initial.retention_size));
  const [excludeDomains, setExcludeDomains] = useState<string[]>(initial.exclude_domains ?? []);
  const [notifyEnabled, setNotifyEnabled] = useState(initial.notify.enabled);
  const [maxScore, setMaxScore] = useState(initial.notify.max_score);
  const [sender, setSender] = useState(initial.notify.sender);
  const [subject, setSubject] = useState(initial.notify.subject);
  const [template, setTemplate] = useState(initial.notify.html_template);
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});

  const save = useAction(async (body: QuarantineSettingsInput) => {
    const { data } = await mailSecurityApi.putQuarantineSettings(body);
    onSaved(data);
    toast.success(t('security.saved'));
  });

  // Con el aviso activo, mail-security valida remitente, asunto y plantilla y responde 422
  // nombrando el campo: el mensaje se pinta junto a ese campo.
  const noticeError = noticeFieldError(save.error);
  const fieldError = (field: NoticeField, local?: string) =>
    local ?? (noticeError?.field === field ? noticeError.message : undefined);
  const editNotice = (setter: (value: string) => void) => (value: string) => {
    setter(value);
    if (noticeError) save.clearError();
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const size = readQuota(maxSize);
    const required = (value: string) =>
      notifyEnabled && !value.trim() ? t('validation.required') : undefined;
    const next = {
      max_size: size.error ?? undefined,
      max_age: validateField(maxAge, rules.required, rules.nonNegativeInteger) ?? undefined,
      retention: validateField(retention, rules.required, rules.nonNegativeInteger) ?? undefined,
      max_score: isDecimal(maxScore) ? undefined : t('validation.decimal'),
      sender:
        required(sender) ??
        (!sender.trim() || normalizeEmail(sender) ? undefined : t('validation.email')),
      subject: required(subject),
      html_template: required(template),
    };
    setErrors(next);
    if (Object.values(next).some(Boolean) || size.bytes === undefined) return;
    await save.run({
      max_size_bytes: size.bytes,
      max_age_days: Number(maxAge),
      retention_size: Number(retention),
      exclude_domains: excludeDomains,
      notify: {
        enabled: notifyEnabled,
        max_score: maxScore.trim(),
        sender: sender.trim(),
        subject: subject.trim(),
        html_template: template,
      },
    });
  };

  const senderError = fieldError('sender', errors.sender);
  const subjectError = fieldError('subject', errors.subject);
  const templateError = fieldError('html_template', errors.html_template);

  return (
    <Card
      title={t('security.quarantineSettings.title')}
      description={t('security.quarantineSettings.description', {
        date: formatDateTime(initial.updated_at),
      })}
    >
      <form className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
        <div className="cf-form__row">
          <QuotaField
            id="q-max-size"
            label={t('security.quarantineSettings.maxSize')}
            value={maxSize}
            onChange={setMaxSize}
            error={errors.max_size}
            hint={t('security.quarantineSettings.maxSizeHint')}
            disabled={!editable}
            required
          />
          <FormField
            label={t('security.quarantineSettings.maxAge')}
            htmlFor="q-max-age"
            required
            error={errors.max_age}
          >
            <Input
              id="q-max-age"
              type="number"
              min={0}
              value={maxAge}
              onChange={(e) => setMaxAge(e.target.value)}
              disabled={!editable}
              invalid={Boolean(errors.max_age)}
            />
          </FormField>
          <FormField
            label={t('security.quarantineSettings.retention')}
            htmlFor="q-retention"
            required
            error={errors.retention}
            hint={t('security.quarantineSettings.retentionHint')}
          >
            <Input
              id="q-retention"
              type="number"
              min={0}
              value={retention}
              onChange={(e) => setRetention(e.target.value)}
              disabled={!editable}
              invalid={Boolean(errors.retention)}
            />
          </FormField>
        </div>
        <FormField
          label={t('security.quarantineSettings.excludeDomains')}
          htmlFor="q-exclude"
          hint={t('security.quarantineSettings.excludeDomainsHint')}
        >
          <ChipsInput
            id="q-exclude"
            values={excludeDomains}
            onChange={setExcludeDomains}
            normalize={normalizeDomainName}
            disabled={!editable}
            removeLabel={(value) => t('common.removeValue', { value })}
            rejectedLabel={(rejected) =>
              t('validation.invalidValues', { list: rejected.join(', ') })
            }
          />
        </FormField>
        <div className="cf-form__section">{t('security.quarantineSettings.notifySection')}</div>
        <Checkbox
          label={t('security.quarantineSettings.notifyEnabled')}
          checked={notifyEnabled}
          onChange={(e) => setNotifyEnabled(e.target.checked)}
          disabled={!editable}
        />
        <div className="cf-form__row">
          <FormField
            label={t('security.quarantineSettings.notifyMaxScore')}
            htmlFor="q-max-score"
            required
            error={errors.max_score}
            hint={t('security.quarantineSettings.notifyMaxScoreHint')}
          >
            <Input
              id="q-max-score"
              inputMode="decimal"
              value={maxScore}
              onChange={(e) => setMaxScore(e.target.value)}
              disabled={!editable}
              invalid={Boolean(errors.max_score)}
            />
          </FormField>
          <FormField
            label={t('security.quarantineSettings.notifySender')}
            htmlFor="q-sender"
            required={notifyEnabled}
            error={senderError}
          >
            <Input
              id="q-sender"
              className="cf-mono"
              value={sender}
              onChange={(e) => editNotice(setSender)(e.target.value)}
              disabled={!editable}
              invalid={Boolean(senderError)}
              aria-describedby={senderError ? 'q-sender-error' : undefined}
            />
          </FormField>
        </div>
        <FormField
          label={t('security.quarantineSettings.notifySubject')}
          htmlFor="q-subject"
          required={notifyEnabled}
          error={subjectError}
        >
          <Input
            id="q-subject"
            value={subject}
            onChange={(e) => editNotice(setSubject)(e.target.value)}
            disabled={!editable}
            invalid={Boolean(subjectError)}
            aria-describedby={subjectError ? 'q-subject-error' : undefined}
          />
        </FormField>
        <div className="cf-split">
          <div className="cf-stack" style={{ gap: 'var(--cf-space-2)' }}>
            <FormField
              label={t('security.quarantineSettings.notifyTemplate')}
              htmlFor="q-template"
              required={notifyEnabled}
              error={templateError}
            >
              <Textarea
                id="q-template"
                mono
                rows={12}
                value={template}
                onChange={(e) => editNotice(setTemplate)(e.target.value)}
                readOnly={!editable}
                invalid={Boolean(templateError)}
                aria-describedby={
                  templateError ? 'q-template-error q-template-vars' : 'q-template-vars'
                }
              />
            </FormField>
            <div id="q-template-vars">
              <NoticeTemplateVariables />
            </div>
          </div>
          <div className="cf-field">
            <span className="cf-field__label">{t('common.preview')}</span>
            <HtmlPreviewFrame
              html={template}
              title={t('security.quarantineSettings.previewTitle')}
              height={260}
            />
          </div>
        </div>
        {save.error && !noticeError ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(save.error)}
          </div>
        ) : null}
        {editable ? (
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
