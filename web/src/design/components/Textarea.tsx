import { forwardRef, type TextareaHTMLAttributes } from 'react';

export interface TextareaProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {
  invalid?: boolean;
  /** Fuente monoespaciada y sin corrector: codigo (sieve, HTML). */
  mono?: boolean;
}

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(function Textarea(
  { invalid = false, mono = false, className, ...rest },
  ref,
) {
  const classes = [
    'cf-textarea',
    mono ? 'cf-textarea--mono' : '',
    invalid ? 'cf-textarea--invalid' : '',
    className ?? '',
  ]
    .filter(Boolean)
    .join(' ');
  return (
    <textarea
      ref={ref}
      className={classes}
      aria-invalid={invalid || undefined}
      spellCheck={mono ? false : undefined}
      {...rest}
    />
  );
});
