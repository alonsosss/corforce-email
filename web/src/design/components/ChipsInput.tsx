import { useId, useState, type ClipboardEvent, type KeyboardEvent } from 'react';
import { IconX } from '../icons';
import { mergeChips } from '@/lib/listInput';

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
}

/**
 * Lista de valores como fichas. Enter, coma, punto y coma o salir del campo confirman lo
 * escrito; lo que no es valido se queda en el campo con su error. Pegar una lista la
 * reparte entera.
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
}: ChipsInputProps) {
  const [draft, setDraft] = useState('');
  const [rejected, setRejected] = useState<string[]>([]);
  const errorId = useId();

  const commit = (text: string) => {
    const merged = mergeChips(values, text, normalize);
    if (merged.values.length !== values.length) onChange(merged.values);
    setRejected(merged.rejected);
    setDraft(merged.rejected.join(' '));
  };

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter' || e.key === ',' || e.key === ';') {
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

  return (
    <div className="cf-stack" style={{ gap: 'var(--cf-space-1)' }}>
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
            setDraft(e.target.value);
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
        />
      </div>
      {rejected.length ? (
        <span id={errorId} className="cf-field__error" role="alert">
          {rejectedLabel(rejected)}
        </span>
      ) : null}
    </div>
  );
}
