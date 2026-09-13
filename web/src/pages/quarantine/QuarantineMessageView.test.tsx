import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { t } from '@/i18n';
import { QuarantineMessageText } from './QuarantineMessageView';

const HOSTILE = [
  'From: atacante@malicioso.test',
  'Subject: Factura pendiente',
  'Content-Type: text/html; charset=utf-8',
  '',
  '<html><body><img src="https://malicioso.test/pixel.png" onerror="window.__pwned=1">',
  '<script>window.__pwned = 2;</script>',
  '<iframe src="https://malicioso.test"></iframe><a href="javascript:alert(1)">Pagar</a>',
  '<form action="https://malicioso.test"><input name="clave"></form>',
  '<style>body { display: none }</style></body></html>',
].join('\r\n');

describe('vista del mensaje en cuarentena', () => {
  it('muestra el mensaje como texto sin crear ningun elemento de su HTML', () => {
    const { container } = render(<QuarantineMessageText source={HOSTILE} />);
    const source = screen.getByTestId('quarantine-message-source');

    expect(source.tagName).toBe('PRE');
    expect(source.textContent).toBe(HOSTILE);
    expect(source.children).toHaveLength(0);
    for (const tag of ['img', 'script', 'iframe', 'a', 'form', 'input', 'style', 'body']) {
      expect(container.querySelector(tag), tag).toBeNull();
    }
    expect((window as unknown as Record<string, unknown>).__pwned).toBeUndefined();
  });

  it('avisa de que es correo sospechoso mostrado sin interpretar', () => {
    render(<QuarantineMessageText source={HOSTILE} />);
    expect(screen.getByText(t('quarantine.message.asText'))).toBeInTheDocument();
  });

  it('recorta un mensaje enorme y dice cuanto se muestra', () => {
    render(<QuarantineMessageText source={'x'.repeat(50)} limit={10} />);
    expect(screen.getByTestId('quarantine-message-source').textContent).toBe('x'.repeat(10));
    expect(
      screen.getByText(t('quarantine.message.truncated', { shown: '10', total: '50' })),
    ).toBeInTheDocument();
  });
});
