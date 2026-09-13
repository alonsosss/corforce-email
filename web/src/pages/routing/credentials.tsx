import { Badge, Checkbox, FormField, PasswordInput } from '@/design/components';
import { t } from '@/i18n';

/** La contrasena de un relayhost o transporte nunca vuelve del API: solo si esta guardada. */
export function PasswordStatus({ configured }: { configured: boolean }) {
  return configured ? (
    <Badge tone="success">{t('routing.password.configured')}</Badge>
  ) : (
    <Badge>{t('routing.password.none')}</Badge>
  );
}

export interface WriteOnlyPasswordProps {
  id: string;
  /** En la edicion: si ya hay una guardada. */
  configured: boolean | null;
  value: string;
  onChange: (value: string) => void;
  clear: boolean;
  onClearChange: (clear: boolean) => void;
}

/**
 * Campo de solo escritura: vacio en la edicion conserva la contrasena guardada; para
 * borrarla hay que pedirlo de forma explicita.
 */
export function WriteOnlyPassword({
  id,
  configured,
  value,
  onChange,
  clear,
  onClearChange,
}: WriteOnlyPasswordProps) {
  const editing = configured !== null;
  return (
    <>
      <FormField
        label={t('common.password')}
        htmlFor={id}
        hint={editing ? t('routing.password.keepHint') : undefined}
      >
        <PasswordInput
          id={id}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={editing && configured ? t('routing.password.unchanged') : undefined}
          disabled={clear}
          autoComplete="new-password"
        />
      </FormField>
      {editing && configured ? (
        <Checkbox
          label={t('routing.password.clear')}
          checked={clear}
          onChange={(e) => onClearChange(e.target.checked)}
        />
      ) : null}
    </>
  );
}

/** Valor de password para un PATCH: "" la borra, ausente la conserva. */
export function passwordPatch(value: string, clear: boolean): string | undefined {
  if (clear) return '';
  return value ? value : undefined;
}
