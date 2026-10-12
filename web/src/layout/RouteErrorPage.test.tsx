import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { RouterProvider, createMemoryRouter } from 'react-router-dom';
import { t } from '@/i18n';
import * as staleChunk from '@/lib/staleChunk';
import { RouteErrorPage } from './RouteErrorPage';

function renderWithError(error: Error) {
  const Broken = () => {
    throw error;
  };
  const router = createMemoryRouter([
    { path: '/', element: <Broken />, errorElement: <RouteErrorPage /> },
  ]);
  render(<RouterProvider router={router} />);
}

describe('pantalla de error de una ruta', () => {
  afterEach(() => vi.restoreAllMocks());

  it('un archivo que no carga recarga una vez y no muestra el error tecnico', () => {
    vi.spyOn(console, 'error').mockImplementation(() => undefined);
    const reload = vi.spyOn(staleChunk, 'reloadOnceForStaleChunk').mockReturnValue(true);
    renderWithError(new TypeError('Failed to fetch dynamically imported module: /assets/A.js'));

    expect(screen.getByRole('heading', { name: t('app.routeError.title') })).toBeInTheDocument();
    expect(reload).toHaveBeenCalledTimes(1);
    expect(screen.queryByText(/Hey developer|Unexpected Application Error/)).toBeNull();
    expect(screen.queryByText(/dynamically imported/)).toBeNull();
  });

  it('otro error no recarga: lo registra y ofrece recargar o ir al inicio', () => {
    const logged = vi.spyOn(console, 'error').mockImplementation(() => undefined);
    const reload = vi.spyOn(staleChunk, 'reloadOnceForStaleChunk');
    renderWithError(new Error('fallo de dibujo'));

    expect(reload).not.toHaveBeenCalled();
    expect(logged).toHaveBeenCalled();
    expect(screen.getByRole('button', { name: t('app.routeError.reload') })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: t('app.routeError.home') })).toHaveAttribute(
      'href',
      '/',
    );
  });
});
