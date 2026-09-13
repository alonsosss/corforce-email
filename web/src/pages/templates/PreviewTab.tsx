import { useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import {
  templatesApi,
  type RenderRequest,
  type RenderedTemplate,
  type TemplateDetail,
  type TemplateVariable,
} from '@/api/templates';
import { errorMessage } from '@/api/messages';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import {
  Button,
  Card,
  DescriptionList,
  EmptyState,
  ErrorState,
  FormField,
  HtmlPreviewFrame,
  Select,
  Skeleton,
} from '@/design/components';
import { t, tEnum } from '@/i18n';
import { VariableValuesForm } from './VariableValuesForm';
import { buildRenderVariables, initialValues, type ValueDraft } from './variables';

/** Version a previsualizar: la pedida en la URL, la publicada o la mas reciente. */
function pickVersion(template: TemplateDetail, requested: number): number | null {
  const numbers = template.versions.map((v) => v.version);
  if (numbers.includes(requested)) return requested;
  if (template.current_version > 0) return template.current_version;
  return numbers.length ? Math.max(...numbers) : null;
}

export function PreviewTab({ template }: { template: TemplateDetail }) {
  const [params] = useSearchParams();
  const [version, setVersion] = useState<number | null>(() =>
    pickVersion(template, Number(params.get('version'))),
  );
  const content = useQuery(
    async () => (version ? (await templatesApi.getVersion(template.id, version)).data : null),
    [template.id, version],
  );

  if (version === null) {
    return (
      <Card>
        <EmptyState title={t('templates.versions.empty')} />
      </Card>
    );
  }

  return (
    <Card title={t('templates.preview.title')} description={t('templates.preview.description')}>
      <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
        <FormField label={t('templates.versions.column.version')} htmlFor="preview-version">
          <Select
            id="preview-version"
            options={[...template.versions]
              .sort((a, b) => b.version - a.version)
              .map((v) => ({
                value: String(v.version),
                label: `${t('templates.versionLabel', { n: v.version })} - ${tEnum('templates.versionStatus', v.status)}`,
              }))}
            value={String(version)}
            onChange={(e) => setVersion(Number(e.target.value))}
          />
        </FormField>
        {content.error ? (
          <ErrorState error={content.error} onRetry={content.reload} />
        ) : !content.data || content.data.version !== version ? (
          <Skeleton lines={4} />
        ) : (
          <PreviewForm
            key={version}
            templateId={template.id}
            version={version}
            variables={content.data.variables ?? []}
          />
        )}
      </div>
    </Card>
  );
}

function PreviewForm({
  templateId,
  version,
  variables,
}: {
  templateId: string;
  version: number;
  variables: readonly TemplateVariable[];
}) {
  const [values, setValues] = useState<ValueDraft>(() => initialValues(variables));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [result, setResult] = useState<RenderedTemplate | null>(null);

  const render = useAction(async (input: RenderRequest) => {
    const { data } = await templatesApi.preview(templateId, input);
    setResult(data);
  });

  const submit = async () => {
    const built = buildRenderVariables(variables, values);
    setErrors(built.errors);
    if (Object.keys(built.errors).length) return;
    await render.run({ version, variables: built.variables });
  };

  return (
    <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
      <form
        className="cf-form"
        noValidate
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <VariableValuesForm
          idPrefix={`preview-${version}`}
          variables={variables}
          values={values}
          onChange={setValues}
          errors={errors}
        />
        {render.error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(render.error)}
          </div>
        ) : null}
        <div className="cf-form__actions">
          <Button type="submit" variant="primary" loading={render.busy}>
            {t('templates.preview.render')}
          </Button>
        </div>
      </form>
      {result ? (
        <div className="cf-stack" style={{ gap: 'var(--cf-space-3)' }}>
          <DescriptionList
            items={[
              { label: t('templates.content.subject'), value: <strong>{result.subject}</strong> },
              {
                label: t('templates.versions.column.version'),
                value: t('templates.versionLabel', { n: result.version }),
              },
            ]}
          />
          <div className="cf-field">
            <span className="cf-field__label">{t('templates.preview.html')}</span>
            <HtmlPreviewFrame html={result.html} title={t('templates.preview.html')} height={480} />
          </div>
          <div className="cf-field">
            <span className="cf-field__label">{t('templates.preview.text')}</span>
            <pre className="cf-pre cf-pre--tall">{result.text}</pre>
          </div>
        </div>
      ) : null}
    </div>
  );
}
