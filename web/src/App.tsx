import { useEffect } from 'react';
import { AppRoutes } from './routes';
import { useAuthStore } from '@/auth/store';
import { ToastProvider } from '@/design/components';
import { applyTheme, resolveTheme } from '@/design/theme';

export function App() {
  useEffect(() => {
    applyTheme(resolveTheme());
    void useAuthStore.getState().hydrate();
  }, []);

  return (
    <ToastProvider>
      <AppRoutes />
    </ToastProvider>
  );
}
