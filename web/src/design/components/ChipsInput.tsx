import { useId, useState, type ClipboardEvent, type KeyboardEvent } from 'react';
import { IconX } from '../icons';
import { mergeChips } from '@/lib/listInput';

export interface ChipSuggestion {
  value: string;
  label: string;
  /** Texto secundario (la direccion bajo el nombre, el origen). */
  detail?: string;
}

export interface ChipsInputProps {
  id?: string;
  values: readonly string[];
  onChange: (values: string[]) => void;
  /** Normaliza un valor escrito; null si no es valido. */
  normalize: (raw: string) => string | null;
  placeholder?: string;
  invalid?: boolean;
  disabled?: boolean;
  /** Nombre accesible del boton que quita un valor. */
  removeLabel: (value: string) => string;
  /** Mensaje cuando parte de lo escrito no es valido. */
  rejectedLabel: (rejected: string[]) => string;
  /** Propuestas para lo que se esta escribiendo; las pide quien usa el campo. */
  suggestions?: readonly ChipSuggestion[];
  /** Cambia el texto que se escribe: quien propone vuelve a buscar. */
  onQueryChange?: (query: string) => void;
  /** Nombre accesible de la lista de propuestas. */
  suggestionsLabel?: string;
}

/**
 * Lista de valores como fichas. Enter, coma, punto y coma o salir del campo confirman lo
 * escrito; lo que no es valido se queda en el campo con su error. Pegar una lista la
 * reparte entera. Con propuestas se comporta como un combobox (ARIA 1.2): flechas para
 * recorrerlas, Enter para elegir y Escape para cerrarlas.
 */
export function ChipsInput({
  id,
  values,
  onChange,
  normalize,
  placeholder,
  invalid = false,
  disabled = false,
  removeLabel,
  rejectedLabel,
  suggestions,
  onQueryChange,
  suggestionsLabel,
}: ChipsInputProps) {
  const [draft, setDraft] = useState('');
  const [rejected, setRejected] = useState<string[]>([]);
  const [active, setActive] = useState(-1);
  const [closed, setClosed] = useState(false);
  const errorId = useId();
  const listId = useId();

  const taken = new Set(values.map((v) => v.toLowerCase()));
  const options = draft.trim()
    ? (suggestions ?? []).filter((s) => !taken.has(s.value.toLowerCase()))
    : [];
  const open = options.length > 0 && !closed && !disabled;

  const updateDraft = (text: string) => {
    setDraft(text);
    setActive(-1);
    setClosed(false);
    onQueryChange?.(text.trim());
  };

  const commit = (text: string) => {
    const merged = mergeChips(values, text, normalize);
    if (merged.values.length !== values.length) onChange(merged.values);
    setRejected(merged.rejected);
    updateDraft(merged.rejected.join(' '));
  };

  const pick = (suggestion: ChipSuggestion) => {
    const value = normalize(suggestion.value);
    if (value && !taken.has(value.toLowerCase())) onChange([...values, value]);
    setRejected([]);
    updateDraft('');
  };

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (open && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
      e.preventDefault();
      const step = e.key === 'ArrowDown' ? 1 : -1;
      setActive((current) => (current + step + options.length) % options.length);
      return;
    }
    if (open && e.key === 'Escape') {
      e.preventDefault();
      setClosed(true);
      return;
    }
    const chosen = open && active >= 0 ? options[active] : undefined;
    if (e.key === 'Enter' && chosen) {
      e.preventDefault();
      pick(chosen);
    } else if (e.key === 'Enter' || e.key === ',' || e.key === ';') {
      e.preventDefault();
      commit(draft);
    } else if (e.key === 'Backspace' && draft === '' && values.length > 0) {
      onChange(values.slice(0, -1));
    }
  };

  const onPaste = (e: ClipboardEvent<HTMLInputElement>) => {
    const text = e.clipboardData.getData('text');
    if (/[\s,;]/.test(text.trim())) {
      e.preventDefault();
      commit(`${draft} ${text}`);
    }
  };

  const hasError = invalid || rejected.length > 0;
  const classes = [
    'cf-chips',
    hasError ? 'cf-chips--invalid' : '',
    disabled ? 'cf-chips--disabled' : '',
  ]
    .filter(Boolean)
    .join(' ');
  const optionId = (index: number) => `${listId}-${index}`;

  return (
    <div className="cf-stack cf-chips-field" style={{ gap: 'var(--cf-space-1)' }}>
      <div className={classes}>
        {values.map((value) => (
          <span key={value} className="cf-chip">
            <span className="cf-chip__text">{value}</span>
            {disabled ? null : (
              <button
                type="button"
                className="cf-chip__remove"
                aria-label={removeLabel(value)}
                onClick={() => onChange(values.filter((v) => v !== value))}
              >
                <IconX size={12} />
              </button>
            )}
          </span>
        ))}
        <input
          id={id}
          className="cf-chips__input"
          value={draft}
          onChange={(e) => {
            updateDraft(e.target.value);
            if (rejected.length) setRejected([]);
          }}
          onKeyDown={onKeyDown}
          onPaste={onPaste}
          onBlur={() => {
            if (draft.trim()) commit(draft);
          }}
          placeholder={values.length ? undefined : placeholder}
          disabled={disabled}
          aria-invalid={hasError || undefined}
          aria-describedby={rejected.length ? errorId : undefined}
          autoComplete="off"
          spellCheck={false}
          {...(suggestions
            ? {
                role: 'combobox',
                'aria-autocomplete': 'list' as const,
                'aria-expanded': open,
                'aria-controls': listId,
                'aria-activedescendant': open && active >= 0 ? optionId(active) : undefined,
              }
            : {})}
        />
      </div>
      {suggestions ? (
        <ul
          id={listId}
          role="listbox"
          aria-label={suggestionsLabel}
          className="cf-chips__suggestions"
          hidden={!open}
        >
          {open
            ? options.map((option, index) => (
                <li
                  key={option.value}
                  id={optionId(index)}
                  role="option"
                  aria-selected={index === active}
                  className="cf-chips__suggestion"
                  // Antes del blur del campo: si no, el blur confirmaria el texto a medias.
                  onMouseDown={(e) => {
                    e.preventDefault();
                    pick(option);
                  }}
                >
                  <span className="cf-chips__suggestion-label">{option.label}</span>
                  {option.detail ? (
                    <span className="cf-chips__suggestion-detail">{option.detail}</span>
                  ) : null}
                </li>
              ))
            : null}
        </ul>
      ) : null}
      {rejected.length ? (
        <span id={errorId} className="cf-field__error" role="alert">
          {rejectedLabel(rejected)}
        </span>
      ) : null}
    </div>
  );
}
