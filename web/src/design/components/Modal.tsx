import { useEffect, useId, useRef, type ReactNode } from 'react';
import { IconX } from '../icons';
import { Button } from './Button';
import { t } from '@/i18n';

export interface ModalProps {
  open: boolean;
  title: string;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  size?: 'md' | 'lg';
  /** Sin cierre por teclado ni por fondo: para dialogos que exigen una decision. */
  dismissible?: boolean;
}

export function Modal({
  open,
  title,
  onClose,
  children,
  footer,
  size = 'md',
  dismissible = true,
}: ModalProps) {
  const titleId = useId();
  const panelRef = useRef<HTMLDivElement>(null);
  // onClose suele ser una funcion nueva en cada render: si fuera dependencia del efecto, cada
  // tecla en un campo del dialogo devolveria el foco al primer campo.
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  useEffect(() => {
    if (!open) return;
    const previous = document.activeElement as HTMLElement | null;
    const panel = panelRef.current;
    const first = panel?.querySelector<HTMLElement>(
      'input, select, textarea, button:not([data-modal-close])',
    );
    (first ?? panel)?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && dismissible) onCloseRef.current();
    };
    document.addEventListener('keydown', onKey);
    const { overflow } = document.body.style;
    document.body.style.overflow = 'hidden';
    return () => {
      document.removeEventListener('keydown', onKey);
      document.body.style.overflow = overflow;
      previous?.focus();
    };
  }, [open, dismissible]);

  if (!open) return null;

  return (
    <div
      className="cf-modal-overlay"
      onMouseDown={(e) => {
        if (dismissible && e.target === e.currentTarget) onClose();
      }}
    >
      <div
        ref={panelRef}
        className={['cf-modal', size === 'lg' ? 'cf-modal--lg' : ''].filter(Boolean).join(' ')}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
      >
        <header className="cf-modal__header">
          <h2 className="cf-modal__title" id={titleId}>
            {title}
          </h2>
          {dismissible ? (
            <Button
              variant="ghost"
              iconOnly
              size="sm"
              icon={<IconX size={16} />}
              onClick={onClose}
              data-modal-close
            >
              {t('common.close')}
            </Button>
          ) : null}
        </header>
        <div className="cf-modal__body">{children}</div>
        {footer ? <footer className="cf-modal__footer">{footer}</footer> : null}
      </div>
    </div>
  );
}
