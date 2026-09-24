import { useMemo, useState } from 'react';
import { errorMessage } from '@/api/messages';
import { useResource } from '@/hooks/useResource';
import { Button, FormField, Input, Modal } from '@/design/components';
import { IconClock } from '@/design/icons';
import { t } from '@/i18n';
import { webmailMeta } from '@/webmail/catalogs';
import {
  formatScheduled,
  fromLocalInput,
  scheduleProblem,
  schedulePresets,
  toLocalInput,
} from './schedule';

export interface ScheduleDialogProps {
  title: string;
  confirmLabel: string;
  /** Hora con la que se abre (cambiar la de un envio ya programado). */
  initial?: Date;
  onClose: () => void;
  /** Recibe la hora en RFC 3339; si falla, el dialogo muestra el error y sigue abierto. */
  onConfirm: (sendAt: string) => Promise<void>;
}

/** Elegir cuando sale un mensaje: atajos o fecha y hora locales, dentro del tope del servicio. */
export function ScheduleDialog({
  title,
  confirmLabel,
  initial,
  onClose,
  onConfirm,
}: ScheduleDialogProps) {
  const maxDays = useResource(webmailMeta).data?.limits.max_scheduled_days ?? null;
  const presets = useMemo(() => schedulePresets(new Date()), []);
  const [value, setValue] = useState(() => toLocalInput(initial ?? presets[0]?.at ?? new Date()));
  const [problem, setProblem] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);

  const submit = async () => {
    const when = fromLocalInput(value);
    const invalid = scheduleProblem(when, new Date(), maxDays);
    setProblem(invalid);
    if (invalid || !when) return;
    setBusy(true);
    setError(null);
    try {
      await onConfirm(when.toISOString());
    } catch (err) {
      setError(err);
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      title={title}
      onClose={() => (busy ? undefined : onClose())}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button
            variant="primary"
            icon={<IconClock size={16} />}
            loading={busy}
            onClick={() => void submit()}
          >
            {confirmLabel}
          </Button>
        </>
      }
    >
      <div className="cf-stack" style={{ gap: 'var(--cf-space-3)' }}>
        <div className="cf-wm-presets" role="group" aria-label={t('webmail.schedule.presets')}>
          {presets.map((preset) => {
            const local = toLocalInput(preset.at);
            return (
              <Button
                key={preset.id}
                size="sm"
                variant={local === value ? 'primary' : 'secondary'}
                aria-pressed={local === value}
                onClick={() => {
                  setValue(local);
                  setProblem(null);
                }}
              >
                {`${t(preset.label)} (${formatScheduled(preset.at)})`}
              </Button>
            );
          })}
        </div>
        <FormField
          label={t('webmail.schedule.when')}
          htmlFor="wm-schedule-at"
          error={problem}
          hint={maxDays !== null ? t('webmail.schedule.maxHint', { days: maxDays }) : undefined}
        >
          <Input
            id="wm-schedule-at"
            type="datetime-local"
            value={value}
            invalid={Boolean(problem)}
            onChange={(e) => {
              setValue(e.target.value);
              setProblem(null);
            }}
          />
        </FormField>
        {error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(error)}
          </div>
        ) : null}
      </div>
    </Modal>
  );
}
