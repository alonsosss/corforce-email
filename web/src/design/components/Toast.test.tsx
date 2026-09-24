import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ToastProvider, useToast, type ToastApi } from './Toast';

let api: ToastApi | null = null;
function Capture() {
  api = useToast();
  return null;
}

function renderToasts() {
  render(
    <ToastProvider>
      <Capture />
    </ToastProvider>,
  );
  return api as ToastApi;
}

describe('avisos con accion', () => {
  afterEach(() => vi.useRealTimers());

  it('la accion se ejecuta una vez y cierra el aviso', async () => {
    const user = userEvent.setup();
    const onAction = vi.fn();
    const toast = renderToasts();
    act(() => {
      toast.show({ kind: 'info', message: 'Enviando', action: { label: 'Deshacer', onAction } });
    });

    await user.click(screen.getByRole('button', { name: 'Deshacer' }));
    expect(onAction).toHaveBeenCalledTimes(1);
    expect(screen.queryByText('Enviando')).toBeNull();
  });

  it('respeta su propio plazo y se puede cerrar antes por su identificador', () => {
    vi.useFakeTimers();
    const toast = renderToasts();
    let id = 0;
    act(() => {
      id = toast.show({ kind: 'info', message: 'Largo', durationMs: 10_000 });
      toast.show({ kind: 'success', message: 'Corto' });
    });
    act(() => vi.advanceTimersByTime(6_000));
    expect(screen.queryByText('Corto')).toBeNull();
    expect(screen.getByText('Largo')).toBeInTheDocument();
    act(() => toast.dismiss(id));
    expect(screen.queryByText('Largo')).toBeNull();
  });
});
