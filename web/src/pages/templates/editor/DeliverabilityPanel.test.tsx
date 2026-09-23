import { act, render, renderHook, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { templatesApi, type CheckResult } from '@/api/templates';
import { t } from '@/i18n';
import { DeliverabilityPanel } from './DeliverabilityPanel';
import { CHECK_DEBOUNCE_MS, useDeliverabilityCheck } from './useDeliverabilityCheck';

const RESULT: CheckResult = {
  passed: false,
  issues: [
    { code: 'missing_alt', severity: 'warning', message: 'Dos imagenes sin alt', count: 2 },
    { code: 'missing_unsubscribe', severity: 'error', message: 'No hay enlace de baja' },
  ],
  stats: { html_bytes: 2048, text_chars: 900, images: 2, links: 5, text_image_ratio: 0.71 },
  spam: {
    available: true,
    score: 1.8,
    required: 15,
    action: 'no action',
    symbols: [{ name: 'MIME_HTML_ONLY', score: 0.2, description: 'Solo HTML' }],
  },
};

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe('panel de entregabilidad', () => {
  it('muestra errores antes que avisos, estadisticas y puntuacion', () => {
    render(
      <DeliverabilityPanel
        state={{ result: RESULT, error: null, checking: false }}
        publishIssues={null}
        compileWarnings={[]}
      />,
    );
    expect(screen.getByText(t('templates.check.failed'))).toBeInTheDocument();
    const titles = screen.getAllByText(/Falta el enlace de baja|Imagenes sin texto alternativo/);
    expect(titles[0]).toHaveTextContent(t('templates.check.issue.missing_unsubscribe'));
    expect(screen.getByText(`${t('templates.check.issue.missing_alt')} (2)`)).toBeInTheDocument();
    expect(screen.getByText('No hay enlace de baja')).toBeInTheDocument();
    expect(
      screen.getByText(t('templates.check.spamValue', { score: '1.8', required: '15.0' })),
    ).toBeInTheDocument();
    expect(screen.getByText('MIME_HTML_ONLY')).toBeInTheDocument();
  });

  it('dice cuando la puntuacion antispam no esta disponible', () => {
    render(
      <DeliverabilityPanel
        state={{ result: { ...RESULT, spam: { available: false } }, error: null, checking: false }}
        publishIssues={null}
        compileWarnings={[]}
      />,
    );
    expect(screen.getByText(t('templates.check.spamUnavailable'))).toBeInTheDocument();
  });

  it('destaca las issues que bloquearon la publicacion', () => {
    render(
      <DeliverabilityPanel
        state={{ result: null, error: null, checking: false }}
        publishIssues={[{ code: 'html_too_large', severity: 'error', message: 'Pesa 140 KB' }]}
        compileWarnings={[]}
      />,
    );
    expect(screen.getByText(t('templates.check.publishBlocked'))).toBeInTheDocument();
    expect(screen.getByText('Pesa 140 KB')).toBeInTheDocument();
  });
});

describe('verificacion en vivo', () => {
  it('espera a que dejen de llegar cambios y verifica una sola vez', async () => {
    vi.useFakeTimers();
    const check = vi.spyOn(templatesApi, 'check').mockResolvedValue(RESULT);
    const build = vi.fn(() => ({ kind: 'marketing' as const, subject: 'S', html: '<p>x</p>' }));

    const { result, rerender } = renderHook(
      ({ revision }) => useDeliverabilityCheck(revision, build, true),
      { initialProps: { revision: 0 } },
    );
    rerender({ revision: 1 });
    rerender({ revision: 2 });
    expect(check).not.toHaveBeenCalled();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(CHECK_DEBOUNCE_MS);
    });
    expect(build).toHaveBeenCalledTimes(1);
    expect(check).toHaveBeenCalledTimes(1);
    expect(result.current.result).toEqual(RESULT);
  });

  it('no verifica mientras el lienzo no esta listo', async () => {
    vi.useFakeTimers();
    const check = vi.spyOn(templatesApi, 'check').mockResolvedValue(RESULT);
    renderHook(() => useDeliverabilityCheck(0, () => null, false));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(CHECK_DEBOUNCE_MS * 2);
    });
    expect(check).not.toHaveBeenCalled();
  });
});
