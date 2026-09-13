import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { HtmlPreviewFrame } from './HtmlPreviewFrame';

const TRACKER = 'https://tracker.test/open.gif';
const HTML = `<p>Correo</p><img src="${TRACKER}"><a href="https://x.test">enlace</a>`;

describe('marco del correo recibido', () => {
  it('la decision sobre imagenes remotas viene de fuera y el marco no ofrece su boton', () => {
    const { rerender } = render(
      <HtmlPreviewFrame html={HTML} title="correo" allowRemoteImages={false} allowLinks />,
    );
    const frame = screen.getByTitle('correo');
    expect(frame.getAttribute('srcdoc')).not.toContain('tracker.test');
    expect(screen.queryByRole('button')).toBeNull();

    rerender(<HtmlPreviewFrame html={HTML} title="correo" allowRemoteImages allowLinks />);
    expect(screen.getByTitle('correo').getAttribute('srcdoc')).toContain(TRACKER);
  });

  it('con enlaces solo admite ventanas nuevas: ni scripts ni mismo origen ni navegar la pagina', () => {
    render(<HtmlPreviewFrame html={HTML} title="correo" allowRemoteImages={false} allowLinks />);
    const tokens = (screen.getByTitle('correo').getAttribute('sandbox') ?? '').split(/\s+/);
    expect(tokens.sort()).toEqual(['allow-popups', 'allow-popups-to-escape-sandbox']);
  });

  it('sin enlaces el sandbox sigue vacio', () => {
    render(<HtmlPreviewFrame html={HTML} title="plantilla" />);
    expect(screen.getByTitle('plantilla')).toHaveAttribute('sandbox', '');
  });
});
