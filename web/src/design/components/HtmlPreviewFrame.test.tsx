import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { t } from '@/i18n';
import { HtmlPreviewFrame } from './HtmlPreviewFrame';

const TRACKER = 'https://tracker.test/open.gif';
const HTML = `<p>Oferta</p><img src="${TRACKER}"><div style="background:url(https://tracker.test/bg.png)">x</div>`;

function frameDoc(): string {
  return screen.getByTitle('vista').getAttribute('srcdoc') ?? '';
}

describe('vista previa de HTML ajeno', () => {
  it('bloquea las imagenes remotas por defecto y dice cuantas', () => {
    render(<HtmlPreviewFrame html={HTML} title="vista" />);
    const frame = screen.getByTitle('vista');
    expect(frame).toHaveAttribute('sandbox', '');
    expect(frameDoc()).not.toContain('tracker.test');
    expect(frameDoc()).toContain('Oferta');
    expect(screen.getByText(t('preview.remoteBlocked', { n: 2 }))).toBeInTheDocument();
  });

  it('las muestra solo al pedirlo en esta vista, y se pueden volver a bloquear', async () => {
    const user = userEvent.setup();
    render(
      <>
        <HtmlPreviewFrame html={HTML} title="vista" />
        <HtmlPreviewFrame html={HTML} title="otra" />
      </>,
    );
    const [first] = screen.getAllByRole('button', { name: t('preview.showRemote') });
    expect(first).toHaveAttribute('aria-pressed', 'false');
    await user.click(first as HTMLElement);

    expect(frameDoc()).toContain(TRACKER);
    expect(screen.getByTitle('otra').getAttribute('srcdoc')).not.toContain('tracker.test');

    const hide = screen.getByRole('button', { name: t('preview.hideRemote') });
    expect(hide).toHaveAttribute('aria-pressed', 'true');
    await user.click(hide);
    expect(frameDoc()).not.toContain('tracker.test');
  });

  it('sin imagenes remotas no ofrece el boton', () => {
    render(<HtmlPreviewFrame html="<p>Solo texto</p>" title="vista" />);
    expect(screen.queryByRole('button')).toBeNull();
  });
});
