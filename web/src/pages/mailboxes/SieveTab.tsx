import { useState, type FormEvent } from 'react';
import {
  directoryMeta,
  mailDirectoryApi,
  type Mailbox,
  type MailboxSieve,
  type SieveFilter,
  type SieveFilterType,
  type SieveScriptInput,
} from '@/api/mailDirectory';
import { errorMessage } from '@/api/messages';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import { useResource } from '@/hooks/useResource';
import {
  Alert,
  Button,
  Card,
  Checkbox,
  ErrorState,
  FormField,
  Input,
  Skeleton,
  Textarea,
  useToast,
} from '@/design/components';
import { t, tEnum } from '@/i18n';

interface ScriptDraft {
  description: string;
  script: string;
  active: boolean;
}

function toDraft(filter: SieveFilter | null): ScriptDraft {
  return {
    description: filter?.script_desc ?? '',
    script: filter?.script_data ?? '',
    active: filter?.active ?? true,
  };
}

/** Un script vacio borra el filtro: el PUT reemplaza siempre los dos. */
function toInput(draft: ScriptDraft): SieveScriptInput | null {
  if (!draft.script.trim()) return null;
  return { script_desc: draft.description.trim(), script_data: draft.script, active: draft.active };
}

function byteLength(text: string): number {
  return new TextEncoder().encode(text).length;
}

export function SieveTab({ mailbox }: { mailbox: Mailbox }) {
  const sieve = useQuery(
    async () => (await mailDirectoryApi.getSieve(mailbox.id)).data,
    [mailbox.id],
  );
  const meta = useResource(directoryMeta);

  const failed = sieve.error ?? meta.error;
  if (failed) {
    return (
      <Card title={t('sieve.title')}>
        <ErrorState
          error={failed}
          onRetry={() => {
            sieve.reload();
            meta.reload();
          }}
        />
      </Card>
    );
  }
  if (!sieve.data || !meta.data) {
    return (
      <Card title={t('sieve.title')}>
        <Skeleton lines={8} />
      </Card>
    );
  }
  const key = `${sieve.data.prefilter?.updated_at ?? ''}|${sieve.data.postfilter?.updated_at ?? ''}`;
  return (
    <SieveForm
      key={key}
      mailbox={mailbox}
      initial={sieve.data}
      maxBytes={meta.data.sieve.script_max_bytes}
      onSaved={sieve.setData}
    />
  );
}

function SieveForm({
  mailbox,
  initial,
  maxBytes,
  onSaved,
}: {
  mailbox: Mailbox;
  initial: MailboxSieve;
  maxBytes: number;
  onSaved: (next: MailboxSieve) => void;
}) {
  const toast = useToast();
  const { can } = useAccess();
  const editable = can(...PERMISSIONS.sieve.update);
  const [prefilter, setPrefilter] = useState(toDraft(initial.prefilter));
  const [postfilter, setPostfilter] = useState(toDraft(initial.postfilter));
  const [errors, setErrors] = useState<Partial<Record<SieveFilterType, string>>>({});

  const save = useAction(async () => {
    const { data } = await mailDirectoryApi.putSieve(mailbox.id, {
      prefilter: toInput(prefilter),
      postfilter: toInput(postfilter),
    });
    onSaved(data);
    toast.success(t('sieve.saved'));
  });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const tooLarge = t('sieve.tooLarge', { kib: maxBytes / 1024 });
    const next = {
      prefilter: byteLength(prefilter.script) > maxBytes ? tooLarge : undefined,
      postfilter: byteLength(postfilter.script) > maxBytes ? tooLarge : undefined,
    };
    setErrors(next);
    if (next.prefilter || next.postfilter) return;
    await save.run();
  };

  return (
    <Card title={t('sieve.title')} description={t('sieve.description')}>
      <form className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
        {!mailbox.sieve_access ? <Alert tone="info">{t('sieve.noManageSieve')}</Alert> : null}
        <div className="cf-split">
          <ScriptEditor
            type="prefilter"
            value={prefilter}
            onChange={setPrefilter}
            error={errors.prefilter}
            disabled={!editable}
          />
          <ScriptEditor
            type="postfilter"
            value={postfilter}
            onChange={setPostfilter}
            error={errors.postfilter}
            disabled={!editable}
          />
        </div>
        {save.error ? (
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

function ScriptEditor({
  type,
  value,
  onChange,
  error,
  disabled,
}: {
  type: SieveFilterType;
  value: ScriptDraft;
  onChange: (next: ScriptDraft) => void;
  error?: string;
  disabled: boolean;
}) {
  return (
    <fieldset className="cf-stack" style={{ gap: 'var(--cf-space-3)', border: 'none', padding: 0 }}>
      <legend className="cf-card__title">{tEnum('sieve.type', type)}</legend>
      <span className="cf-text-sm cf-text-secondary">{tEnum('sieve.typeHint', type)}</span>
      <FormField label={t('common.description')} htmlFor={`sieve-${type}-desc`}>
        <Input
          id={`sieve-${type}-desc`}
          value={value.description}
          onChange={(e) => onChange({ ...value, description: e.target.value })}
          disabled={disabled}
        />
      </FormField>
      <FormField
        label={t('sieve.script')}
        htmlFor={`sieve-${type}-script`}
        error={error}
        hint={t('sieve.emptyDeletes')}
      >
        <Textarea
          id={`sieve-${type}-script`}
          mono
          rows={14}
          value={value.script}
          onChange={(e) => onChange({ ...value, script: e.target.value })}
          readOnly={disabled}
          invalid={Boolean(error)}
        />
      </FormField>
      <Checkbox
        label={t('sieve.active')}
        checked={value.active}
        onChange={(e) => onChange({ ...value, active: e.target.checked })}
        disabled={disabled}
      />
    </fieldset>
  );
}
