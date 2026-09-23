import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import {
  templatesApi,
  templatesMeta,
  type TemplateVariable,
  type TestSendResult,
  type VariableValue,
} from '@/api/templates';
import { transactionalApi } from '@/api/transactional';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import {
  Alert,
  ChipsInput,
  ErrorState,
  FormField,
  Input,
  Modal,
  Select,
  Skeleton,
} from '@/design/components';
import { rules } from '@/lib/validate';
import { t, tEnum } from '@/i18n';
import { paths } from '@/paths';
import { FormModal } from '@/pages/shared/FormModal';
import { senderAddress, sendableDomains, testVariables } from './testSend';
import { VariableValuesForm } from './VariableValuesForm';
import { initialValues, type ValueDraft } from './variables';

export interface VersionTestSendProps {
  templateId: string;
  version: number;
  onClose: () => void;
}

/** Carga la version guardada y el tope de destinatarios, y abre el envio de prueba. */
export function VersionTestSend({ templateId, version, onClose }: VersionTestSendProps) {
  const data = useQuery(async () => {
    const [saved, meta] = await Promise.all([
      templatesApi.getVersion(templateId, version).then((res) => res.data),
      templatesMeta.get(),
    ]);
    return { variables: saved.variables ?? [], maxRecipients: meta.limits.max_test_recipients };
  }, [templateId, version]);

  if (!data.data) {
    return (
      <Modal open title={t('templates.testSend.title', { n: version })} onClose={onClose}>
        {data.error ? (
          <ErrorState error={data.error} onRetry={data.reload} />
        ) : (
          <Skeleton lines={4} />
        )}
      </Modal>
    );
  }
  return (
    <TestSendModal
      templateId={templateId}
      version={version}
      variables={data.data.variables}
      maxRecipients={data.data.maxRecipients}
      onClose={onClose}
    />
  );
}

export interface TestSendModalProps {
  templateId: string;
  version: number;
  variables: readonly TemplateVariable[];
  maxRecipients: number;
  onClose: () => void;
}

/**
 * Envio de prueba de una version por transactional, con un remitente de los dominios de envio
 * verificados de la empresa. Las variables vacias las completa el servicio con valores de
 * ejemplo; el asunto llega con el prefijo de prueba.
 */
export function TestSendModal({
  templateId,
  version,
  variables,
  maxRecipients,
  onClose,
}: TestSendModalProps) {
  const { can } = useAccess();
  const canReadDomains = can(...PERMISSIONS.sendingDomains.read);
  const domains = useQuery(
    async () => (canReadDomains ? sendableDomains(await transactionalApi.sendingDomains()) : []),
    [canReadDomains],
  );

  const [localPart, setLocalPart] = useState('');
  const [domain, setDomain] = useState('');
  const [fromName, setFromName] = useState('');
  const [to, setTo] = useState<string[]>([]);
  const [values, setValues] = useState<ValueDraft>(() => initialValues(variables));
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});
  const [valueErrors, setValueErrors] = useState<Record<string, string>>({});
  const [result, setResult] = useState<TestSendResult | null>(null);

  const options = useMemo(
    () => (domains.data ?? []).map((d) => ({ value: d, label: `@${d}` })),
    [domains.data],
  );
  const selectedDomain = domain || options[0]?.value || '';

  const action = useAction(
    async (from: string, recipients: string[], sample: Record<string, VariableValue>) => {
      setResult(
        await templatesApi.testSend(templateId, version, {
          from: { email: from, name: fromName.trim() || undefined },
          to: recipients,
          variables: sample,
        }),
      );
    },
  );

  const submit = async () => {
    const from = senderAddress(localPart, selectedDomain);
    const built = testVariables(variables, values);
    const next = {
      from: from && rules.email(from) === null ? undefined : t('templates.testSend.fromInvalid'),
      to:
        to.length === 0 || to.length > maxRecipients
          ? t('templates.testSend.count', { n: maxRecipients })
          : undefined,
    };
    setErrors(next);
    setValueErrors(built.errors);
    if (next.from || next.to || Object.keys(built.errors).length || !from) return;
    setResult(null);
    await action.run(from, to, built.variables);
  };

  const noDomains = domains.data !== null && options.length === 0;
  const suppressed = result?.suppressed ?? [];

  return (
    <FormModal
      id="template-test-send"
      title={t('templates.testSend.title', { n: version })}
      submitLabel={t('templates.testSend.submit')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={submit}
      submitDisabled={!canReadDomains || noDomains || domains.data === null}
      size="lg"
    >
      <p className="cf-text-secondary">{t('templates.testSend.description')}</p>
      {!canReadDomains ? (
        <Alert tone="warning">{t('templates.testSend.noDomainPermission')}</Alert>
      ) : domains.error ? (
        <ErrorState error={domains.error} onRetry={domains.reload} />
      ) : domains.data === null ? (
        <Skeleton lines={2} />
      ) : noDomains ? (
        <Alert tone="warning" title={t('templates.testSend.noDomains')}>
          <Link to={paths.domains}>{t('templates.testSend.goToDomains')}</Link>
        </Alert>
      ) : (
        <div className="cf-form__row">
          <FormField
            label={t('templates.testSend.fromLocal')}
            htmlFor="test-send-local"
            required
            error={errors.from}
            hint={t('templates.testSend.fromHint')}
          >
            <Input
              id="test-send-local"
              value={localPart}
              onChange={(e) => setLocalPart(e.target.value)}
              invalid={Boolean(errors.from)}
              autoComplete="off"
            />
          </FormField>
          <FormField label={t('templates.testSend.fromDomain')} htmlFor="test-send-domain" required>
            <Select
              id="test-send-domain"
              options={options}
              value={selectedDomain}
              onChange={(e) => setDomain(e.target.value)}
            />
          </FormField>
          <FormField label={t('templates.testSend.fromName')} htmlFor="test-send-name">
            <Input
              id="test-send-name"
              value={fromName}
              onChange={(e) => setFromName(e.target.value)}
            />
          </FormField>
        </div>
      )}
      <FormField
        label={t('templates.testSend.to')}
        htmlFor="test-send-to"
        required
        error={errors.to}
        hint={t('templates.testSend.count', { n: maxRecipients })}
      >
        <ChipsInput
          id="test-send-to"
          values={to}
          onChange={setTo}
          normalize={(raw) => (rules.email(raw) === null ? raw.trim() : null)}
          invalid={Boolean(errors.to)}
          removeLabel={(value) => t('common.removeValue', { value })}
          rejectedLabel={(list) => t('validation.invalidAddresses', { list: list.join(', ') })}
        />
      </FormField>
      <div className="cf-form__section">{t('templates.testSend.values')}</div>
      <span className="cf-text-sm cf-text-secondary">{t('templates.testSend.valuesHint')}</span>
      <VariableValuesForm
        idPrefix={`test-send-${version}`}
        variables={variables}
        values={values}
        onChange={setValues}
        errors={valueErrors}
      />
      {result ? (
        <Alert
          tone={suppressed.length ? 'warning' : 'success'}
          title={t('templates.testSend.done')}
        >
          <span>{t('templates.testSend.accepted', { n: result.messages.length })}</span>
          {suppressed.map((s) => (
            <span key={s.email} className="cf-text-sm">
              {t('templates.testSend.suppressed', {
                email: s.email,
                reason: tEnum('suppression.reason', s.reason),
              })}
            </span>
          ))}
        </Alert>
      ) : null}
    </FormModal>
  );
}
