import { forwardRef, useState, type InputHTMLAttributes } from 'react';
import { IconEye, IconEyeOff } from '../icons';
import { t } from '@/i18n';

export interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  invalid?: boolean;
}

export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  { invalid = false, className, ...rest },
  ref,
) {
  const classes = ['cf-input', invalid ? 'cf-input--invalid' : '', className ?? '']
    .filter(Boolean)
    .join(' ');
  return <input ref={ref} className={classes} aria-invalid={invalid || undefined} {...rest} />;
});

export const PasswordInput = forwardRef<HTMLInputElement, InputProps>(
  function PasswordInput(props, ref) {
    const [visible, setVisible] = useState(false);
    const Icon = visible ? IconEyeOff : IconEye;
    return (
      <div className="cf-input-group">
        <Input ref={ref} type={visible ? 'text' : 'password'} {...props} />
        <button
          type="button"
          className="cf-btn cf-btn--ghost cf-btn--icon cf-btn--sm cf-input-group__addon"
          onClick={() => setVisible((v) => !v)}
          aria-label={visible ? t('common.hidePassword') : t('common.showPassword')}
          tabIndex={-1}
        >
          <Icon size={16} />
        </button>
      </div>
    );
  },
);
