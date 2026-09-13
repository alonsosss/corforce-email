import type { ReactNode } from 'react';

export interface FormFieldProps {
  label: string;
  htmlFor?: string;
  hint?: ReactNode;
  error?: string | null;
  required?: boolean;
  children: ReactNode;
}

export function FormField({ label, htmlFor, hint, error, required, children }: FormFieldProps) {
  const errorId = htmlFor && error ? `${htmlFor}-error` : undefined;
  return (
    <div className="cf-field">
      <label className="cf-field__label" htmlFor={htmlFor}>
        {label}
        {required ? (
          <span className="cf-field__required" aria-hidden="true">
            *
          </span>
        ) : null}
      </label>
      {children}
      {error ? (
        <span className="cf-field__error" id={errorId} role="alert">
          {error}
        </span>
      ) : hint ? (
        <span className="cf-field__hint">{hint}</span>
      ) : null}
    </div>
  );
}
