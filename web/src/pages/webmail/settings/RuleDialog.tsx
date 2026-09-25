import { useState } from 'react';
import {
  RULE_ACTIONS,
  RULE_FIELDS,
  RULE_OPERATORS,
  type FilterLimits,
  type MailRule,
  type RuleAction,
  type RuleCondition,
  type WebmailFolder,
} from '@/api/webmail';
import { Button, Checkbox, FormField, Input, Modal, Select } from '@/design/components';
import { IconPlus, IconX } from '@/design/icons';
import { t } from '@/i18n';
import { useWebmailStore } from '@/webmail/store';
import { orderFolders } from '../folders';
import {
  actionOfType,
  apiFieldError,
  emptyCondition,
  emptyRule,
  filtersErrorMessage,
  ruleProblems,
  type FieldErrors,
} from './filters';

export interface RuleDialogProps {
  /** null: regla nueva. */
  initial: MailRule | null;
  /** Posicion de la regla en la lista: la de los errores rules[i].* del servicio. */
  index: number;
  limits: FilterLimits;
  folders: readonly WebmailFolder[];
  onClose: () => void;
  onSave: (rule: MailRule) => Promise<void>;
}

export function RuleDialog({ initial, index, limits, folders, onClose, onSave }: RuleDialogProps) {
  const targets = orderFolders(folders).filter((item) => item.folder.selectable);
  const firstFolder = targets[0]?.folder.name ?? '';
  const ownAddress = useWebmailStore((s) => s.session?.username ?? '');
  const [rule, setRule] = useState<MailRule>(() => initial ?? emptyRule(firstFolder));
  const [problems, setProblems] = useState<FieldErrors>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);

  const update = (patch: Partial<MailRule>) => {
    setRule((current) => ({ ...current, ...patch }));
    setProblems({});
  };
  const setCondition = (i: number, patch: Partial<RuleCondition>) =>
    update({ conditions: rule.conditions.map((c, j) => (j === i ? { ...c, ...patch } : c)) });
  const setAction = (i: number, action: RuleAction) =>
    update({ actions: rule.actions.map((a, j) => (j === i ? action : a)) });

  const submit = async () => {
    const found = ruleProblems(rule, limits, ownAddress);
    setProblems(found);
    if (Object.keys(found).length) return;
    setBusy(true);
    setError(null);
    try {
      await onSave({
        ...rule,
        name: rule.name.trim(),
        conditions: rule.conditions.map((c) => ({ ...c, value: c.value.trim() })),
      });
    } catch (err) {
      const byField = apiFieldError(err, `rules[${index}].`);
      if (byField) setProblems(byField);
      else setError(err);
      setBusy(false);
    }
  };

  const folderOptions = targets.map((item) => ({
    value: item.folder.name,
    label: `${'  '.repeat(item.depth)}${item.label}`,
  }));

  return (
    <Modal
      open
      size="lg"
      title={t(initial ? 'webmail.rules.editTitle' : 'webmail.rules.newTitle')}
      onClose={() => (busy ? undefined : onClose())}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button variant="primary" loading={busy} onClick={() => void submit()}>
            {t('common.save')}
          </Button>
        </>
      }
    >
      <form
        className="cf-form"
        noValidate
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <FormField label={t('webmail.rules.name')} htmlFor="wm-rule-name" error={problems.name}>
          <Input
            id="wm-rule-name"
            value={rule.name}
            invalid={Boolean(problems.name)}
            onChange={(e) => update({ name: e.target.value })}
          />
        </FormField>
        <FormField label={t('webmail.rules.match')} htmlFor="wm-rule-match">
          <Select
            id="wm-rule-match"
            value={rule.match}
            options={[
              { value: 'all', label: t('webmail.rules.matchAll') },
              { value: 'any', label: t('webmail.rules.matchAny') },
            ]}
            onChange={(e) => update({ match: e.target.value === 'any' ? 'any' : 'all' })}
          />
        </FormField>

        <fieldset className="cf-wm-fieldset">
          <legend className="cf-field__label">{t('webmail.rules.conditions')}</legend>
          {rule.conditions.map((condition, i) => {
            const key = `conditions[${i}].value`;
            return (
              <div key={i} className="cf-wm-rule-row">
                <Select
                  aria-label={t('webmail.rules.field')}
                  value={condition.field}
                  options={RULE_FIELDS.map((field) => ({
                    value: field,
                    label: t(`webmail.rules.field.${field}`),
                  }))}
                  onChange={(e) =>
                    setCondition(i, {
                      field: RULE_FIELDS.find((f) => f === e.target.value) ?? condition.field,
                    })
                  }
                />
                <Select
                  aria-label={t('webmail.rules.operator')}
                  value={condition.op}
                  options={RULE_OPERATORS.map((op) => ({
                    value: op,
                    label: t(`webmail.rules.op.${op}`),
                  }))}
                  onChange={(e) =>
                    setCondition(i, {
                      op: RULE_OPERATORS.find((o) => o === e.target.value) ?? condition.op,
                    })
                  }
                />
                <div className="cf-wm-rule-row__value">
                  <Input
                    id={`wm-rule-${key}`}
                    aria-label={t('webmail.rules.value')}
                    value={condition.value}
                    invalid={Boolean(problems[key])}
                    aria-describedby={problems[key] ? `wm-rule-${key}-error` : undefined}
                    onChange={(e) => setCondition(i, { value: e.target.value })}
                  />
                  {problems[key] ? (
                    <span id={`wm-rule-${key}-error`} className="cf-field__error" role="alert">
                      {problems[key]}
                    </span>
                  ) : null}
                </div>
                <Button
                  size="sm"
                  variant="ghost"
                  iconOnly
                  icon={<IconX size={16} />}
                  disabled={rule.conditions.length <= 1}
                  onClick={() => update({ conditions: rule.conditions.filter((_, j) => j !== i) })}
                >
                  {t('webmail.rules.removeCondition')}
                </Button>
              </div>
            );
          })}
          {problems.conditions ? (
            <span className="cf-field__error" role="alert">
              {problems.conditions}
            </span>
          ) : null}
          <div>
            <Button
              size="sm"
              variant="ghost"
              icon={<IconPlus size={16} />}
              disabled={rule.conditions.length >= limits.max_conditions}
              onClick={() => update({ conditions: [...rule.conditions, emptyCondition()] })}
            >
              {t('webmail.rules.addCondition')}
            </Button>
          </div>
        </fieldset>

        <fieldset className="cf-wm-fieldset">
          <legend className="cf-field__label">{t('webmail.rules.actions')}</legend>
          {rule.actions.map((action, i) => {
            const folderKey = `actions[${i}].folder`;
            const addressKey = `actions[${i}].address`;
            const detailError =
              problems[`actions[${i}].type`] ?? problems[folderKey] ?? problems[addressKey];
            return (
              <div key={i} className="cf-wm-rule-row">
                <Select
                  aria-label={t('webmail.rules.action')}
                  value={action.type}
                  options={RULE_ACTIONS.map((type) => ({
                    value: type,
                    label: t(`webmail.rules.action.${type}`),
                  }))}
                  onChange={(e) => {
                    const type = RULE_ACTIONS.find((a) => a === e.target.value);
                    if (type) setAction(i, actionOfType(type, firstFolder));
                  }}
                />
                <div className="cf-wm-rule-row__value">
                  {action.type === 'move' ? (
                    <Select
                      aria-label={t('webmail.rules.folder')}
                      value={action.folder}
                      placeholder={t('common.select')}
                      options={folderOptions}
                      invalid={Boolean(problems[folderKey])}
                      onChange={(e) => setAction(i, { ...action, folder: e.target.value })}
                    />
                  ) : null}
                  {action.type === 'forward' ? (
                    <>
                      <Input
                        type="email"
                        aria-label={t('webmail.rules.forwardTo')}
                        placeholder={t('webmail.compose.recipientsPlaceholder')}
                        value={action.address}
                        invalid={Boolean(problems[addressKey])}
                        onChange={(e) => setAction(i, { ...action, address: e.target.value })}
                      />
                      <Checkbox
                        label={t('webmail.forwarding.keepCopy')}
                        checked={action.keep_copy}
                        onChange={(e) => setAction(i, { ...action, keep_copy: e.target.checked })}
                      />
                    </>
                  ) : null}
                  {detailError ? (
                    <span className="cf-field__error" role="alert">
                      {detailError}
                    </span>
                  ) : null}
                </div>
                <Button
                  size="sm"
                  variant="ghost"
                  iconOnly
                  icon={<IconX size={16} />}
                  disabled={rule.actions.length <= 1}
                  onClick={() => update({ actions: rule.actions.filter((_, j) => j !== i) })}
                >
                  {t('webmail.rules.removeAction')}
                </Button>
              </div>
            );
          })}
          {problems.actions ? (
            <span className="cf-field__error" role="alert">
              {problems.actions}
            </span>
          ) : null}
          <div>
            <Button
              size="sm"
              variant="ghost"
              icon={<IconPlus size={16} />}
              disabled={rule.actions.length >= limits.max_actions}
              onClick={() => update({ actions: [...rule.actions, actionOfType('mark_read')] })}
            >
              {t('webmail.rules.addAction')}
            </Button>
          </div>
        </fieldset>

        <Checkbox
          label={t('webmail.rules.stop')}
          checked={rule.stop}
          onChange={(e) => update({ stop: e.target.checked })}
        />
        <Checkbox
          label={t('webmail.rules.enabled')}
          checked={rule.enabled}
          onChange={(e) => update({ enabled: e.target.checked })}
        />
        {problems.general || error ? (
          <div className="cf-form__error" role="alert">
            {problems.general ?? filtersErrorMessage(error)}
          </div>
        ) : null}
      </form>
    </Modal>
  );
}
