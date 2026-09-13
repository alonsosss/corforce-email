import { useMemo, useState, type FormEvent } from 'react';
import { SUPPRESSION_MAX_CHECK, suppressionApi, type SuppressedAddress } from '@/api/suppression';
import { errorMessage } from '@/api/messages';
import { useAction } from '@/hooks/useAction';
import {
  Alert,
  Button,
  Card,
  DataTable,
  FormField,
  Textarea,
  type Column,
} from '@/design/components';
import { splitLines } from '@/lib/listInput';
import { getLocale, t } from '@/i18n';
import { ReasonBadge } from './suppressionReason';

interface CheckResult {
  checked: number;
  suppressed: SuppressedAddress[];
}

const columns: Column<SuppressedAddress>[] = [
  {
    key: 'email',
    header: t('common.email'),
    render: (s) => <strong className="cf-mono">{s.email}</strong>,
  },
  {
    key: 'reason',
    header: t('suppression.column.reason'),
    render: (s) => <ReasonBadge reason={s.reason} />,
  },
];

export function CheckTab() {
  const [text, setText] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<CheckResult | null>(null);
  const lines = useMemo(() => splitLines(text), [text]);
  const format = new Intl.NumberFormat(getLocale());

  const action = useAction(async (emails: string[]) => {
    const suppressed = await suppressionApi.check(emails);
    setResult({ checked: emails.length, suppressed });
  });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const next =
      lines.length === 0
        ? t('suppression.import.emptyList')
        : lines.length > SUPPRESSION_MAX_CHECK
          ? t('suppression.check.tooMany', { max: format.format(SUPPRESSION_MAX_CHECK) })
          : null;
    setError(next);
    if (next) return;
    setResult(null);
    await action.run(lines);
  };

  return (
    <div className="cf-stack">
      <Card title={t('suppression.check.title')} description={t('suppression.check.description')}>
        <form className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
          <FormField
            label={t('suppression.import.addresses')}
            htmlFor="suppression-check"
            required
            error={error}
            hint={t('suppression.import.count', {
              n: format.format(lines.length),
              max: format.format(SUPPRESSION_MAX_CHECK),
            })}
          >
            <Textarea
              id="suppression-check"
              mono
              rows={8}
              value={text}
              onChange={(e) => setText(e.target.value)}
              invalid={Boolean(error)}
              placeholder={t('suppression.import.placeholder')}
            />
          </FormField>
          {action.error ? (
            <div className="cf-form__error" role="alert">
              {errorMessage(action.error)}
            </div>
          ) : null}
          <div className="cf-form__actions">
            <Button type="submit" variant="primary" loading={action.busy}>
              {t('suppression.check.submit')}
            </Button>
          </div>
        </form>
      </Card>
      {result ? (
        <Card flush>
          <div style={{ padding: 'var(--cf-space-4) var(--cf-space-5)' }}>
            <Alert tone={result.suppressed.length ? 'warning' : 'success'}>
              {t('suppression.check.summary', {
                n: format.format(result.suppressed.length),
                total: format.format(result.checked),
              })}
            </Alert>
          </div>
          {result.suppressed.length ? (
            <DataTable columns={columns} rows={result.suppressed} rowKey={(s) => s.email} />
          ) : null}
        </Card>
      ) : null}
    </div>
  );
}
