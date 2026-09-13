import { useState, type FormEvent } from 'react';
import {
  CELL_STATUSES,
  type Cell,
  type CellStatus,
  type CreateCellRequest,
  type UpdateCellRequest,
} from '@/api/organization';
import { ERROR_CODES } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import { useAction } from '@/hooks/useAction';
import { Button, FormField, Input, Modal, Select } from '@/design/components';
import { hasErrors, rules, validateField, type FieldErrors } from '@/lib/validate';
import { t, tEnum } from '@/i18n';

type Field = 'code' | 'region' | 'db_host' | 'db_port';

export type CellFormProps =
  | {
      mode: 'create';
      open: boolean;
      onClose: () => void;
      onSubmit: (input: CreateCellRequest) => Promise<void>;
    }
  | {
      mode: 'edit';
      open: boolean;
      cell: Cell;
      onClose: () => void;
      onSubmit: (input: UpdateCellRequest) => Promise<void>;
    };

const FORM_ID = 'cell-form';

export function CellForm(props: CellFormProps) {
  const { mode, open, onClose } = props;
  const initial = mode === 'edit' ? props.cell : null;
  const [code, setCode] = useState(initial?.code ?? '');
  const [region, setRegion] = useState(initial?.region ?? '');
  const [dbHost, setDbHost] = useState(initial?.db_host ?? '');
  const [dbPort, setDbPort] = useState(initial ? String(initial.db_port) : '');
  const [status, setStatus] = useState<CellStatus>(initial?.status ?? 'active');
  const [errors, setErrors] = useState<FieldErrors<Field>>({});

  const action = useAction(async () => {
    if (props.mode === 'create') {
      await props.onSubmit({
        code: code.trim(),
        region: region.trim(),
        db_host: dbHost.trim(),
        db_port: Number(dbPort),
      });
    } else {
      await props.onSubmit({
        region: region.trim() !== props.cell.region ? region.trim() : undefined,
        status: status !== props.cell.status ? status : undefined,
      });
    }
  });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const next: FieldErrors<Field> = {
      region: validateField(region, rules.required, rules.maxLength(63)) ?? undefined,
    };
    if (mode === 'create') {
      next.code = validateField(code, rules.required, rules.maxLength(63)) ?? undefined;
      next.db_host = validateField(dbHost, rules.required, rules.maxLength(255)) ?? undefined;
      next.db_port = validateField(dbPort, rules.required, rules.port) ?? undefined;
    }
    setErrors(next);
    if (hasErrors(next)) return;
    await action.run();
  };

  return (
    <Modal
      open={open}
      title={mode === 'create' ? t('cells.form.createTitle') : t('cells.form.editTitle')}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose} disabled={action.busy}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" form={FORM_ID} variant="primary" loading={action.busy}>
            {mode === 'create' ? t('common.create') : t('common.save')}
          </Button>
        </>
      }
    >
      <form id={FORM_ID} className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
        <div className="cf-form__row">
          <FormField
            label={t('cells.form.code')}
            htmlFor="cell-code"
            required={mode === 'create'}
            error={errors.code}
            hint={t('cells.form.codeHint')}
          >
            <Input
              id="cell-code"
              className="cf-mono"
              value={code}
              onChange={(e) => setCode(e.target.value)}
              invalid={Boolean(errors.code)}
              disabled={mode === 'edit'}
              autoComplete="off"
            />
          </FormField>
          <FormField
            label={t('cells.form.region')}
            htmlFor="cell-region"
            required
            error={errors.region}
          >
            <Input
              id="cell-region"
              value={region}
              onChange={(e) => setRegion(e.target.value)}
              invalid={Boolean(errors.region)}
            />
          </FormField>
        </div>
        {mode === 'create' ? (
          <div className="cf-form__row">
            <FormField
              label={t('cells.form.dbHost')}
              htmlFor="cell-host"
              required
              error={errors.db_host}
            >
              <Input
                id="cell-host"
                className="cf-mono"
                value={dbHost}
                onChange={(e) => setDbHost(e.target.value)}
                invalid={Boolean(errors.db_host)}
                autoComplete="off"
              />
            </FormField>
            <FormField
              label={t('cells.form.dbPort')}
              htmlFor="cell-port"
              required
              error={errors.db_port}
            >
              <Input
                id="cell-port"
                type="number"
                min={1}
                max={65535}
                value={dbPort}
                onChange={(e) => setDbPort(e.target.value)}
                invalid={Boolean(errors.db_port)}
              />
            </FormField>
          </div>
        ) : (
          <FormField label={t('common.status')} htmlFor="cell-status">
            <Select
              id="cell-status"
              options={CELL_STATUSES.map((s) => ({ value: s, label: tEnum('cells.status', s) }))}
              value={status}
              onChange={(e) => setStatus(e.target.value as CellStatus)}
            />
          </FormField>
        )}
        {action.error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(action.error, { [ERROR_CODES.CONFLICT]: 'cells.exists' })}
          </div>
        ) : null}
      </form>
    </Modal>
  );
}
