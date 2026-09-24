import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

// jsdom no maqueta: se comprueban las reglas de las que depende lo que se ve en el navegador.
// Se leen del disco porque Vitest entrega vacío el CSS importado, también con ?raw.
const read = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8');
const components = read('../components.css');
const webmail = read('../../pages/webmail/webmail.css');

function block(css: string, selector: string): string {
  const start = css.indexOf(`${selector} {`);
  expect(start, selector).toBeGreaterThanOrEqual(0);
  return css.slice(start, css.indexOf('}', start));
}

describe('estilos del sistema de diseno', () => {
  it('la fila de pestanas solo se desplaza en horizontal y sin barra visible', () => {
    const tabs = block(components, '.cf-tabs');
    expect(tabs).toMatch(/overflow-x: auto;/);
    expect(tabs).toMatch(/overflow-y: hidden;/);
    expect(tabs).toMatch(/scrollbar-width: none;/);
    expect(components).toMatch(/\.cf-tabs::-webkit-scrollbar \{\s*display: none;/);
    // Un margen negativo en la pestana desborda en vertical y hace aparecer la barra.
    expect(block(components, '.cf-tab')).not.toMatch(/margin-bottom: -/);
  });

  it('la casilla ofrece 24px de objetivo tactil y 44px con puntero grueso', () => {
    expect(components).toMatch(/--cf-checkbox-target: 24px;/);
    expect(components).toMatch(
      /@media \(pointer: coarse\) \{\s*\.cf-checkbox \{\s*--cf-checkbox-target: 44px;/,
    );
    const input = block(components, '.cf-checkbox input');
    expect(input).toMatch(/width: var\(--cf-checkbox-target\);/);
    expect(input).toMatch(/height: var\(--cf-checkbox-target\);/);
  });

  it('la ayuda de atajos se oculta en pantallas tactiles sin teclado', () => {
    expect(webmail).toMatch(
      /@media \(hover: none\) and \(pointer: coarse\) \{\s*\.cf-btn\.cf-wm__help \{\s*display: none;/,
    );
  });
});
