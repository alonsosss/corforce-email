import { useState } from 'react';
import { API_KEY_ERRORS, apiKeysApi, type ApiKeyScope, type CreatedApiKey } from '@/api/apiKeys';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import { Alert, Checkbox, FormField, Input } from '@/design/components';
import { localToRfc3339 } from '@/lib/format';
import { rules, validateField } from '@/lib/validate';
import { t } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { ResourceGate } from '@/pages/shared/ResourceGate';
import { scopeKey, scopeLabel } from './apiKeyScopes';

// Limites de domain.APIKey en access-control (nombre de 1 a 100 caracteres).
const NAME_MAX_LENGTH = 100;

const ERROR_OVERRIDES = {
  [API_KEY_ERRORS.SCOPE_NOT_GRANTABLE]: 'apiKeys.error.scopeNotGrantable',
  [API_KEY_ERRORS.API_KEY_LIMIT]: 'apiKeys.error.limit',
} as const;

interface FieldErrors {
  name?: string | null;
  scopes?: string | null;
  expiresAt?: string | null;
}

export interface ApiKeyFormProps {
  onClose: () => void;
  onCreated: (result: CreatedApiKey) => void;
}

export function ApiKeyForm({ onClose, onCreated }: ApiKeyFormProps) {
  const scopes = useQuery(() => apiKeysApi.scopes(), []);
  return (
    <ResourceGate resource={scopes} modal={{ title: t('apiKeys.form.title'), onClose }}>
      {(catalog) => <ApiKeyFormBody catalog={catalog} onClose={onClose} onCreated={onCreated} />}
    </ResourceGate>
  );
}

function ApiKeyFormBody({
  catalog,
  onClose,
  onCreated,
}: ApiKeyFormProps & { catalog: ApiKeyScope[] }) {
  const [name, setName] = useState('');
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [expiresAt, setExpiresAt] = useState('');
  const [errors, setErrors] = useState<FieldErrors>({});

  const action = useAction(async (expires: string | null) => {
    const result = await apiKeysApi.create({
      name: name.trim(),
      scopes: catalog
        .filter((s) => selected.has(scopeKey(s)))
        .map(({ module, resource, action: act }) => ({ module, resource, action: act })),
      expires_at: expires,
    });
    onCreated(result);
  });

  const toggle = (key: string, on: boolean) => {
    const next = new Set(selected);
    if (on) next.add(key);
    else next.delete(key);
    setSelected(next);
  };

  const submit = async () => {
    const expires = expiresAt ? (localToRfc3339(expiresAt) ?? null) : null;
    const next: FieldErrors = {
      name: validateField(name, rules.required, rules.maxLength(NAME_MAX_LENGTH)),
      scopes: selected.size === 0 ? t('apiKeys.form.scopesRequired') : null,
      expiresAt:
        expiresAt && (!expires || new Date(expires).getTime() <= Date.now())
          ? t('apiKeys.form.expiresFuture')
          : null,
    };
    setErrors(next);
    if (next.name || next.scopes || next.expiresAt) return;
    await action.run(expires);
  };

  return (
    <FormModal
      id="api-key-form"
      title={t('apiKeys.form.title')}
      submitLabel={t('common.create')}
      busy={action.busy}
      error={action.error}
      errorOverrides={ERROR_OVERRIDES}
      onClose={onClose}
      onSubmit={submit}
      size="lg"
      submitDisabled={catalog.length === 0}
    >
      <FormField
        label={t('common.name')}
        htmlFor="api-key-name"
        required
        error={errors.name}
        hint={t('apiKeys.form.nameHint')}
      >
        <Input
          id="api-key-name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          invalid={Boolean(errors.name)}
          maxLength={NAME_MAX_LENGTH}
          autoComplete="off"
        />
      </FormField>
      {catalog.length === 0 ? (
        <Alert tone="warning">{t('apiKeys.form.noScopes')}</Alert>
      ) : (
        <fieldset className="cf-checklist">
          <legend className="cf-field__label">{t('apiKeys.form.scopes')}</legend>
          <span className="cf-text-muted cf-text-sm">{t('apiKeys.form.scopesHint')}</span>
          <div className="cf-checklist__items">
            {catalog.map((scope) => {
              const key = scopeKey(scope);
              return (
                <Checkbox
                  key={key}
                  checked={selected.has(key)}
                  onChange={(e) => toggle(key, e.target.checked)}
                  label={scopeLabel(scope)}
                  title={key}
                />
              );
            })}
          </div>
          {errors.scopes ? (
            <span className="cf-field__error" role="alert">
              {errors.scopes}
            </span>
          ) : null}
        </fieldset>
      )}
      <FormField
        label={t('apiKeys.form.expiresAt')}
        htmlFor="api-key-expires"
        error={errors.expiresAt}
        hint={t('apiKeys.form.expiresHint')}
      >
        <Input
          id="api-key-expires"
          type="datetime-local"
          value={expiresAt}
          onChange={(e) => setExpiresAt(e.target.value)}
          invalid={Boolean(errors.expiresAt)}
        />
      </FormField>
    </FormModal>
  );
}
