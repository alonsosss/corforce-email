import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import { IconAlertCircle, IconCheckCircle, IconInfo, IconX } from '../icons';
import { t } from '@/i18n';

type ToastKind = 'success' | 'error' | 'info';

export interface ToastAction {
  label: string;
  onAction: () => void;
}

export interface ToastOptions {
  kind: ToastKind;
  message: string;
  /** Un boton junto al mensaje; pulsarlo cierra el aviso. */
  action?: ToastAction;
  /** Cuanto sigue visible; por defecto, el plazo comun de los avisos. */
  durationMs?: number;
}

interface ToastItem extends ToastOptions {
  id: number;
}

export interface ToastApi {
  success: (message: string) => void;
  error: (message: string) => void;
  info: (message: string) => void;
  /** Aviso con opciones; devuelve su identificador para cerrarlo antes de tiempo. */
  show: (options: ToastOptions) => number;
  dismiss: (id: number) => void;
}

const ToastContext = createContext<ToastApi | null>(null);

const AUTO_DISMISS_MS = 5000;

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);
  const counter = useRef(0);
  const timers = useRef(new Map<number, number>());

  const dismiss = useCallback((id: number) => {
    const timer = timers.current.get(id);
    if (timer !== undefined) window.clearTimeout(timer);
    timers.current.delete(id);
    setItems((current) => current.filter((item) => item.id !== id));
  }, []);

  const show = useCallback(
    (options: ToastOptions) => {
      const id = ++counter.current;
      setItems((current) => [...current, { ...options, id }]);
      timers.current.set(
        id,
        window.setTimeout(() => dismiss(id), options.durationMs ?? AUTO_DISMISS_MS),
      );
      return id;
    },
    [dismiss],
  );

  useEffect(() => {
    const pending = timers.current;
    return () => pending.forEach((timer) => window.clearTimeout(timer));
  }, []);

  const api = useMemo<ToastApi>(
    () => ({
      success: (message) => void show({ kind: 'success', message }),
      error: (message) => void show({ kind: 'error', message }),
      info: (message) => void show({ kind: 'info', message }),
      show,
      dismiss,
    }),
    [show, dismiss],
  );

  return (
    <ToastContext.Provider value={api}>
      {children}
      <div className="cf-toast-viewport" aria-live="polite">
        {items.map((item) => (
          <div key={item.id} className={`cf-toast cf-toast--${item.kind}`} role="status">
            <ToastIcon kind={item.kind} />
            <span className="cf-toast__message">{item.message}</span>
            {item.action ? (
              <button
                type="button"
                className="cf-btn cf-btn--secondary cf-btn--sm"
                onClick={() => {
                  dismiss(item.id);
                  item.action?.onAction();
                }}
              >
                {item.action.label}
              </button>
            ) : null}
            <button
              type="button"
              className="cf-btn cf-btn--ghost cf-btn--icon cf-btn--sm"
              onClick={() => dismiss(item.id)}
              aria-label={t('common.close')}
            >
              <IconX size={14} />
            </button>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}

function ToastIcon({ kind }: { kind: ToastKind }) {
  if (kind === 'success') return <IconCheckCircle size={18} />;
  if (kind === 'error') return <IconAlertCircle size={18} />;
  return <IconInfo size={18} />;
}

export function useToast(): ToastApi {
  const ctx = useContext(ToastContext);
  if (!ctx) throw new Error('useToast requiere ToastProvider');
  return ctx;
}
