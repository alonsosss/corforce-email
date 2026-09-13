import { forwardRef, type InputHTMLAttributes, type ReactNode } from 'react';

export interface CheckboxProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'type'> {
  label?: ReactNode;
}

export const Checkbox = forwardRef<HTMLInputElement, CheckboxProps>(function Checkbox(
  { label, className, ...rest },
  ref,
) {
  return (
    <label className={['cf-checkbox', className ?? ''].filter(Boolean).join(' ')}>
      <input ref={ref} type="checkbox" {...rest} />
      {label ? <span>{label}</span> : null}
    </label>
  );
});
