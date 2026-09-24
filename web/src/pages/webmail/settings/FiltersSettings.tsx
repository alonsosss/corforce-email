import { useState } from 'react';
import {
  webmailApi,
  type Forwarding,
  type MailFilters,
  type MailRule,
  type WebmailFolder,
} from '@/api/webmail';
import { errorMessage } from '@/api/messages';
import { useQuery } from '@/hooks/useQuery';
import {
  Alert,
  Badge,
  Button,
  Card,
  Checkbox,
  ChipsInput,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  FormField,
  Skeleton,
  useToast,
} from '@/design/components';
import { IconChevronDown, IconChevronUp, IconEdit, IconPlus, IconTrash } from '@/design/icons';
import { t } from '@/i18n';
import { normalizeRecipient } from '../compose';
import { apiFieldError, forwardingProblems, toFiltersInput, type FieldErrors } from './filters';
import { RuleDialog } from './RuleDialog';

/**
 * Reglas y reenvio comparten recurso (GET/PUT /filters): cada pestana cambia su parte y
 * guarda el conjunto con la otra tal como la devolvio el servicio.
 */
export function FiltersSettings({ part }: { part: 'rules' | 'forwarding' }) {
  const filters = useQuery((signal) => webmailApi.filters(signal), []);
  const folders = useQuery(
    (signal) => (part === 'rules' ? webmailApi.folders(signal) : Promise.resolve([])),
    [part],
  );
  const title = t(part === 'rules' ? 'webmail.rules.title' : 'webmail.forwarding.title');

  if (filters.error && !filters.data) {
    return (
      <Card title={title}>
        <ErrorState
          error={filters.error}
          title={t('webmail.rules.unavailable')}
          onRetry={filters.reload}
        />
      </Card>
    );
  }
  if (!filters.data) {
    return (
      <Card title={title}>
        <Skeleton lines={6} />
      </Card>
    );
  }
  const save = async (rules: MailRule[], forwarding: Forwarding) => {
    const saved = await webmailApi.setFilters(toFiltersInput(rules, forwarding));
    filters.setData(saved);
  };
  return part === 'rules' ? (
    <RulesCard filters={filters.data} folders={folders.data ?? []} onSave={save} />
  ) : (
    <ForwardingCard
      key={filters.data.forwarding.addresses.join(',')}
      filters={filters.data}
      onSave={save}
    />
  );
}

type Save = (rules: MailRule[], forwarding: Forwarding) => Promise<void>;

function RulesCard({
  filters,
  folders,
  onSave,
}: {
  filters: MailFilters;
  folders: readonly WebmailFolder[];
  onSave: Save;
}) {
  const toast = useToast();
  const { rules, forwarding, limits } = filters;
  const [editing, setEditing] = useState<number | null>(null);
  const [removing, setRemoving] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const full = rules.length >= limits.max_rules;

  const saveList = async (next: MailRule[]) => {
    setBusy(true);
    setError(null);
    try {
      await onSave(next, forwarding);
      return true;
    } catch (err) {
      setError(err);
      return false;
    } finally {
      setBusy(false);
    }
  };

  const move = (index: number, step: -1 | 1) => {
    const next = [...rules];
    const [rule] = next.splice(index, 1);
    if (!rule) return;
    next.splice(index + step, 0, rule);
    void saveList(next);
  };

  return (
    <Card
      title={t('webmail.rules.title')}
      description={t('webmail.rules.description')}
      actions={
        <Button
          size="sm"
          variant="primary"
          icon={<IconPlus size={16} />}
          disabled={full || busy}
          onClick={() => setEditing(rules.length)}
        >
          {t('webmail.rules.new')}
        </Button>
      }
    >
      <div className="cf-stack" style={{ gap: 'var(--cf-space-3)' }}>
        {full ? (
          <Alert tone="info">{t('webmail.rules.full', { max: limits.max_rules })}</Alert>
        ) : null}
        {error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(error)}
          </div>
        ) : null}
        {rules.length === 0 ? (
          <EmptyState title={t('webmail.rules.empty')} description={t('webmail.rules.emptyHint')} />
        ) : (
          <ol className="cf-wm-rules">
            {rules.map((rule, index) => (
              <li key={rule.id || index} className="cf-wm-rule">
                <Checkbox
                  aria-label={t('webmail.rules.toggle', { name: rule.name })}
                  checked={rule.enabled}
                  disabled={busy}
                  onChange={(e) =>
                    void saveList(
                      rules.map((r, i) => (i === index ? { ...r, enabled: e.target.checked } : r)),
                    )
                  }
                />
                <div className="cf-wm-rule__body">
                  <span className="cf-wm-rule__name">{rule.name}</span>
                  <span className="cf-text-sm cf-text-secondary">
                    {t('webmail.rules.summary', {
                      conditions: rule.conditions.length,
                      actions: rule.actions.length,
                    })}
                  </span>
                </div>
                {!rule.enabled ? <Badge>{t('common.disabled')}</Badge> : null}
                <div className="cf-wm-rule__actions">
                  <Button
                    size="sm"
                    variant="ghost"
                    iconOnly
                    icon={<IconChevronUp size={16} />}
                    disabled={busy || index === 0}
                    onClick={() => move(index, -1)}
                  >
                    {t('webmail.rules.up', { name: rule.name })}
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    iconOnly
                    icon={<IconChevronDown size={16} />}
                    disabled={busy || index === rules.length - 1}
                    onClick={() => move(index, 1)}
                  >
                    {t('webmail.rules.down', { name: rule.name })}
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    iconOnly
                    icon={<IconEdit size={16} />}
                    disabled={busy}
                    onClick={() => setEditing(index)}
                  >
                    {t('webmail.rules.edit', { name: rule.name })}
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    iconOnly
                    icon={<IconTrash size={16} />}
                    disabled={busy}
                    onClick={() => setRemoving(index)}
                  >
                    {t('webmail.rules.remove', { name: rule.name })}
                  </Button>
                </div>
              </li>
            ))}
          </ol>
        )}
      </div>
      {editing !== null ? (
        <RuleDialog
          initial={rules[editing] ?? null}
          index={editing}
          limits={limits}
          folders={folders}
          onClose={() => setEditing(null)}
          onSave={async (rule) => {
            const next =
              editing < rules.length
                ? rules.map((r, i) => (i === editing ? rule : r))
                : [...rules, rule];
            await onSave(next, forwarding);
            toast.success(t('webmail.rules.saved'));
            setEditing(null);
          }}
        />
      ) : null}
      <ConfirmDialog
        open={removing !== null}
        title={t('webmail.rules.removeTitle')}
        message={t('webmail.rules.removeConfirm', { name: rules[removing ?? 0]?.name ?? '' })}
        confirmLabel={t('common.delete')}
        danger
        onCancel={() => setRemoving(null)}
        onConfirm={async () => {
          await onSave(
            rules.filter((_, i) => i !== removing),
            forwarding,
          );
          toast.success(t('webmail.rules.removed'));
          setRemoving(null);
        }}
      />
    </Card>
  );
}

function ForwardingCard({ filters, onSave }: { filters: MailFilters; onSave: Save }) {
  const toast = useToast();
  const { limits } = filters;
  const [enabled, setEnabled] = useState(filters.forwarding.enabled);
  const [addresses, setAddresses] = useState(filters.forwarding.addresses);
  const [keepCopy, setKeepCopy] = useState(filters.forwarding.keep_copy);
  const [problems, setProblems] = useState<FieldErrors>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);

  const submit = async () => {
    const forwarding = { enabled, addresses, keep_copy: keepCopy };
    const found = forwardingProblems(forwarding, limits);
    setProblems(found);
    if (Object.keys(found).length) return;
    setBusy(true);
    setError(null);
    try {
      await onSave(filters.rules, forwarding);
      toast.success(t('webmail.forwarding.saved'));
    } catch (err) {
      const byField = apiFieldError(err, 'forwarding.');
      if (byField) setProblems(byField);
      else setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title={t('webmail.forwarding.title')} description={t('webmail.forwarding.description')}>
      <form
        className="cf-form"
        noValidate
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <Checkbox
          label={t('webmail.forwarding.enabled')}
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
          disabled={busy}
        />
        <FormField
          label={t('webmail.forwarding.addresses')}
          htmlFor="wm-forward-addresses"
          error={problems.addresses ?? null}
          hint={t('webmail.forwarding.addressesHint', { max: limits.max_forward_addresses })}
        >
          <ChipsInput
            id="wm-forward-addresses"
            values={addresses}
            onChange={(values) => {
              setAddresses(values);
              setProblems({});
            }}
            normalize={normalizeRecipient}
            invalid={Boolean(problems.addresses)}
            disabled={busy}
            placeholder={t('webmail.compose.recipientsPlaceholder')}
            removeLabel={(value) => t('common.removeValue', { value })}
            rejectedLabel={(rejected) =>
              t('webmail.compose.invalidAddresses', { list: rejected.join(', ') })
            }
          />
        </FormField>
        <Checkbox
          label={t('webmail.forwarding.keepCopy')}
          checked={keepCopy}
          onChange={(e) => setKeepCopy(e.target.checked)}
          disabled={busy}
        />
        <Alert tone="info">{t('webmail.forwarding.spamNote')}</Alert>
        {problems.general || error ? (
          <div className="cf-form__error" role="alert">
            {problems.general ?? errorMessage(error)}
          </div>
        ) : null}
        <div className="cf-form__actions">
          <Button type="submit" variant="primary" loading={busy}>
            {t('common.save')}
          </Button>
        </div>
      </form>
    </Card>
  );
}
