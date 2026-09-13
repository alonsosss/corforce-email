import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import { IconAlertCircle, IconCheckCircle, IconInfo, IconX } from '../icons';
import { t } from '@/i18n';

type ToastKind = 'success' | 'error' | 'info';

interface ToastItem {
  id: number;
  kind: ToastKind;
  message: string;
}

export interface ToastApi {
  success: (message: string) => void;
  error: (message: string) => void;
  info: (message: string) => void;
}

const ToastContext = createContext<ToastApi | null>(null);

const AUTO_DISMISS_MS = 5000;

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);
  const counter = useRef(0);

  const dismiss = useCallback((id: number) => {
    setItems((current) => current.filter((item) => item.id !== id));
  }, []);

  const push = useCallback(
    (kind: ToastKind, message: string) => {
      const id = ++counter.current;
      setItems((current) => [...current, { id, kind, message }]);
      window.setTimeout(() => dismiss(id), AUTO_DISMISS_MS);
    },
    [dismiss],
  );

  const api = useMemo<ToastApi>(
    () => ({
      success: (m) => push('success', m),
      error: (m) => push('error', m),
      info: (m) => push('info', m),
    }),
    [push],
  );

  return (
    <ToastContext.Provider value={api}>
      {children}
      <div className="cf-toast-viewport" aria-live="polite">
        {items.map((item) => (
          <div key={item.id} className={`cf-toast cf-toast--${item.kind}`} role="status">
            <ToastIcon kind={item.kind} />
            <span className="cf-toast__message">{item.message}</span>
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
