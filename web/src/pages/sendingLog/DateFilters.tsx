import { FormField, Input } from '@/design/components';
import { t } from '@/i18n';

/** Desde y hasta, en dias locales; cambiar cualquiera vuelve a la primera pagina. */
export function DateFilters({
  from,
  until,
  onFrom,
  onUntil,
  onChange,
}: {
  from: string;
  until: string;
  onFrom: (v: string) => void;
  onUntil: (v: string) => void;
  onChange: () => void;
}) {
  return (
    <>
      <FormField label={t('sendingLog.filter.from')} htmlFor="sending-log-from">
        <Input
          id="sending-log-from"
          type="date"
          value={from}
          onChange={(e) => {
            onFrom(e.target.value);
            onChange();
          }}
        />
      </FormField>
      <FormField label={t('sendingLog.filter.until')} htmlFor="sending-log-until">
        <Input
          id="sending-log-until"
          type="date"
          value={until}
          onChange={(e) => {
            onUntil(e.target.value);
            onChange();
          }}
        />
      </FormField>
    </>
  );
}
