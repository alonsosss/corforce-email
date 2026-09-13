import { useState } from 'react';
import { mailSecurityApi, type SpamScore } from '@/api/mailSecurity';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { Badge, FormField, Input, type Column } from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { t, tEnum } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { ListTab, type ResourceFormProps } from '@/pages/shared/ResourceTab';
import { ObjectField } from './ObjectField';
import { isDecimal, normalizePolicyObject, policyObjectKind } from './policyObject';

const loadScores = () => mailSecurityApi.listSpamScores();

const columns: Column<SpamScore>[] = [
  {
    key: 'object',
    header: t('security.object'),
    render: (s) => (
      <span className="cf-inline">
        <strong className="cf-mono">{s.object}</strong>
        <Badge>{tEnum('security.objectKind', policyObjectKind(s.object))}</Badge>
      </span>
    ),
  },
  { key: 'low', header: t('security.spamScores.low'), align: 'right', render: (s) => s.low_score },
  {
    key: 'high',
    header: t('security.spamScores.high'),
    align: 'right',
    render: (s) => s.high_score,
  },
  { key: 'updated', header: t('common.updatedAt'), render: (s) => formatDateTime(s.updated_at) },
];

export function SpamScoresTab() {
  const { can } = useAccess();
  const canUpdate = can(...PERMISSIONS.spamScores.update);
  return (
    <ListTab
      load={loadScores}
      rowKey={(s) => s.object}
      columns={columns}
      Form={SpamScoreForm}
      canCreate={canUpdate}
      canUpdate={canUpdate}
      canDelete={can(...PERMISSIONS.spamScores.delete)}
      remove={(s) => mailSecurityApi.deleteSpamScore(s.object)}
      texts={{
        title: t('security.spamScores.title'),
        description: t('security.spamScores.description'),
        create: t('security.spamScores.new'),
        empty: t('security.spamScores.empty'),
        created: t('security.saved'),
        updated: t('security.saved'),
        deleted: t('security.deleted'),
        deleteTitle: t('security.spamScores.delete'),
        deleteConfirm: (s) => t('security.spamScores.deleteConfirm', { object: s.object }),
      }}
    />
  );
}

function SpamScoreForm({ item, onClose, onSaved }: ResourceFormProps<SpamScore>) {
  const [object, setObject] = useState(item?.object ?? '');
  const [low, setLow] = useState(item?.low_score ?? '');
  const [high, setHigh] = useState(item?.high_score ?? '');
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});

  const action = useAction(async (target: string) => {
    await mailSecurityApi.putSpamScore(target, { low_score: low.trim(), high_score: high.trim() });
    onSaved();
  });

  const submit = async () => {
    const target = item ? item.object : normalizePolicyObject(object);
    const next = {
      object: target ? undefined : t('security.objectInvalid'),
      low: isDecimal(low) ? undefined : t('validation.decimal'),
      high: isDecimal(high) ? undefined : t('validation.decimal'),
    };
    setErrors(next);
    if (!target || next.low || next.high) return;
    await action.run(target);
  };

  return (
    <FormModal
      id="spam-score-form"
      title={item ? t('security.spamScores.editTitle') : t('security.spamScores.createTitle')}
      submitLabel={t('common.save')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={submit}
    >
      <ObjectField
        id="spam-score-object"
        value={object}
        onChange={setObject}
        error={errors.object}
        disabled={Boolean(item)}
      />
      <div className="cf-form__row">
        <FormField
          label={t('security.spamScores.low')}
          htmlFor="spam-score-low"
          required
          error={errors.low}
          hint={t('security.spamScores.lowHint')}
        >
          <Input
            id="spam-score-low"
            inputMode="decimal"
            value={low}
            onChange={(e) => setLow(e.target.value)}
            invalid={Boolean(errors.low)}
          />
        </FormField>
        <FormField
          label={t('security.spamScores.high')}
          htmlFor="spam-score-high"
          required
          error={errors.high}
          hint={t('security.spamScores.highHint')}
        >
          <Input
            id="spam-score-high"
            inputMode="decimal"
            value={high}
            onChange={(e) => setHigh(e.target.value)}
            invalid={Boolean(errors.high)}
          />
        </FormField>
      </div>
    </FormModal>
  );
}
