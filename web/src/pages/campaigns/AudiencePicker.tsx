import type { Audience } from '@/api/campaigns';
import { Alert, Checkbox } from '@/design/components';
import { t } from '@/i18n';

export interface NamedOption {
  id: string;
  name: string;
}

export interface AudienceOptions {
  /** null si el rol no puede leer listas o segmentos. */
  lists: NamedOption[] | null;
  segments: NamedOption[] | null;
}

function toggle(ids: string[], id: string, on: boolean): string[] {
  return on ? [...ids.filter((x) => x !== id), id] : ids.filter((x) => x !== id);
}

function CheckList({
  legend,
  options,
  selected,
  onChange,
  disabled,
  empty,
}: {
  legend: string;
  options: NamedOption[];
  selected: string[];
  onChange: (id: string, on: boolean) => void;
  disabled?: boolean;
  empty: string;
}) {
  return (
    <fieldset className="cf-checklist" disabled={disabled}>
      <legend className="cf-field__label">{legend}</legend>
      {options.length === 0 ? (
        <span className="cf-text-muted cf-text-sm">{empty}</span>
      ) : (
        <div className="cf-checklist__items">
          {options.map((o) => (
            <Checkbox
              key={o.id}
              label={o.name}
              checked={selected.includes(o.id)}
              onChange={(e) => onChange(o.id, e.target.checked)}
            />
          ))}
        </div>
      )}
    </fieldset>
  );
}

/**
 * Audiencia de una campana: listas y segmentos que suman, y segmentos que restan. Un
 * segmento no puede estar incluido y excluido a la vez: marcarlo en un lado lo quita del
 * otro. Contacts solo entrega contactos activos con consentimiento vigente.
 */
export function AudiencePicker({
  value,
  onChange,
  options,
  disabled,
  error,
}: {
  value: Audience;
  onChange: (next: Audience) => void;
  options: AudienceOptions;
  disabled?: boolean;
  error?: string;
}) {
  return (
    <div className="cf-stack" style={{ gap: 'var(--cf-space-3)' }}>
      {options.lists === null || options.segments === null ? (
        <Alert tone="info">{t('campaigns.audience.limited')}</Alert>
      ) : null}
      <div className="cf-form__row">
        {options.lists ? (
          <CheckList
            legend={t('campaigns.audience.lists')}
            options={options.lists}
            selected={value.list_ids}
            disabled={disabled}
            empty={t('contacts.lists.none')}
            onChange={(id, on) => onChange({ ...value, list_ids: toggle(value.list_ids, id, on) })}
          />
        ) : null}
        {options.segments ? (
          <CheckList
            legend={t('campaigns.audience.segments')}
            options={options.segments}
            selected={value.segment_ids}
            disabled={disabled}
            empty={t('segments.empty')}
            onChange={(id, on) =>
              onChange({
                ...value,
                segment_ids: toggle(value.segment_ids, id, on),
                exclude_segment_ids: on
                  ? value.exclude_segment_ids.filter((x) => x !== id)
                  : value.exclude_segment_ids,
              })
            }
          />
        ) : null}
        {options.segments ? (
          <CheckList
            legend={t('campaigns.audience.exclude')}
            options={options.segments}
            selected={value.exclude_segment_ids}
            disabled={disabled}
            empty={t('segments.empty')}
            onChange={(id, on) =>
              onChange({
                ...value,
                exclude_segment_ids: toggle(value.exclude_segment_ids, id, on),
                segment_ids: on ? value.segment_ids.filter((x) => x !== id) : value.segment_ids,
              })
            }
          />
        ) : null}
      </div>
      {error ? (
        <span className="cf-field__error" role="alert">
          {error}
        </span>
      ) : null}
    </div>
  );
}
