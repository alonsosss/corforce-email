import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { isTypingTarget, useShortcuts } from './shortcuts';
import { newUnread, readNotifyPreference, writeNotifyPreference } from './notifications';

function Harness({ onCompose }: { onCompose: () => void }) {
  useShortcuts({ c: onCompose });
  return (
    <>
      <input aria-label="campo" />
      <textarea aria-label="area" />
      <div contentEditable suppressContentEditableWarning aria-label="editor" role="textbox" />
      <input type="checkbox" aria-label="casilla" />
    </>
  );
}

describe('atajos de teclado', () => {
  it('disparan fuera de los campos y nunca mientras se escribe', async () => {
    const user = userEvent.setup();
    const onCompose = vi.fn();
    render(<Harness onCompose={onCompose} />);

    await user.type(screen.getByLabelText('campo'), 'c');
    await user.type(screen.getByLabelText('area'), 'c');
    fireEvent.keyDown(screen.getByRole('textbox', { name: 'editor' }), { key: 'c' });
    expect(onCompose).not.toHaveBeenCalled();

    fireEvent.keyDown(document.body, { key: 'c' });
    expect(onCompose).toHaveBeenCalledTimes(1);

    // Con modificadores es un atajo del navegador (copiar), no del webmail.
    fireEvent.keyDown(document.body, { key: 'c', ctrlKey: true });
    expect(onCompose).toHaveBeenCalledTimes(1);
  });

  it('una casilla no cuenta como escribir; un dialogo abierto si bloquea', () => {
    const onCompose = vi.fn();
    render(<Harness onCompose={onCompose} />);
    expect(isTypingTarget(screen.getByLabelText('casilla'))).toBe(false);

    const dialog = document.createElement('div');
    dialog.setAttribute('aria-modal', 'true');
    document.body.appendChild(dialog);
    fireEvent.keyDown(document.body, { key: 'c' });
    expect(onCompose).not.toHaveBeenCalled();
    dialog.remove();
  });
});

describe('atajos con una redaccion abierta', () => {
  function WithCompose({ onArchive }: { onArchive: () => void }) {
    useShortcuts({ e: onArchive });
    return (
      <main tabIndex={-1} aria-label="lector">
        <button type="button">fuera</button>
        <section data-keyboard-owner="focus" tabIndex={-1} aria-label="respuesta">
          <p>hueco del formulario</p>
        </section>
      </main>
    );
  }

  it('un clic en un hueco de la redaccion deja el foco dentro y las teclas no archivan', async () => {
    const user = userEvent.setup();
    const onArchive = vi.fn();
    render(<WithCompose onArchive={onArchive} />);

    await user.click(screen.getByText('hueco del formulario'));
    expect(document.activeElement).toBe(screen.getByLabelText('respuesta'));
    await user.keyboard('e');
    expect(onArchive).not.toHaveBeenCalled();
  });

  it('con el foco caido en un contenedor de la redaccion tampoco hay atajos', () => {
    const onArchive = vi.fn();
    render(<WithCompose onArchive={onArchive} />);
    screen.getByLabelText('lector').focus();
    fireEvent.keyDown(document.activeElement!, { key: 'e' });
    expect(onArchive).not.toHaveBeenCalled();

    // Fuera de la redaccion, en un control propio, el atajo sigue funcionando.
    screen.getByRole('button', { name: 'fuera' }).focus();
    fireEvent.keyDown(document.activeElement!, { key: 'e' });
    expect(onArchive).toHaveBeenCalledTimes(1);
  });
});

describe('preferencia de avisos de escritorio', () => {
  afterEach(() => vi.restoreAllMocks());

  it('se guarda en este navegador', () => {
    writeNotifyPreference(true);
    expect(readNotifyPreference()).toBe(true);
    writeNotifyPreference(false);
    expect(readNotifyPreference()).toBe(false);
  });

  it('con el almacenamiento bloqueado no rompe: queda desactivada', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new DOMException('bloqueado', 'SecurityError');
    });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('bloqueado', 'SecurityError');
    });
    expect(() => writeNotifyPreference(true)).not.toThrow();
    expect(readNotifyPreference()).toBe(false);
  });

  it('solo cuenta como nuevos los no leidos que suben', () => {
    expect(newUnread(2, 5)).toBe(3);
    expect(newUnread(5, 2)).toBe(0);
    expect(newUnread(null, 2)).toBe(0);
  });
});
