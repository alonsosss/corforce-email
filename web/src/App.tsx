import { useEffect } from 'react';
import { AppRoutes } from './routes';
import { ToastProvider } from '@/design/components';
import { applyTheme, resolveTheme } from '@/design/theme';

export function App() {
  useEffect(() => {
    applyTheme(resolveTheme());
  }, []);

  return (
    <ToastProvider>
      <AppRoutes />
    </ToastProvider>
  );
}
