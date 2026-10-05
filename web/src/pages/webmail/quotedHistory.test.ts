import { describe, expect, it } from 'vitest';
import { splitQuotedHtml, splitQuotedText } from './quotedHistory';

describe('historial citado en HTML', () => {
  it('pliega la cita de Gmail con su atribucion y deja lo nuevo', () => {
    const split = splitQuotedHtml(
      '<div dir="ltr">Bbbb</div><br><div class="gmail_quote"><div class="gmail_attr">El dom, Carlos escribió:</div><blockquote>dasdasd</blockquote></div>',
    );
    expect(split?.main).toContain('Bbbb');
    expect(split?.main).not.toContain('dasdasd');
    expect(split?.quoted).toContain('Carlos escribió:');
    expect(split?.quoted).toContain('dasdasd');
  });

  it('en Apple Mail incluye la linea de atribucion que precede al blockquote', () => {
    const split = splitQuotedHtml(
      '<div>Gracias</div><div>On Sun, Luis wrote:</div><blockquote type="cite">Hola</blockquote>',
    );
    expect(split?.main).toContain('Gracias');
    expect(split?.main).not.toContain('Luis wrote');
    expect(split?.quoted).toContain('Luis wrote');
  });

  it('sin marca de cliente pliega el ultimo blockquote y su atribucion', () => {
    const split = splitQuotedHtml(
      '<div>Listo</div><div><br></div><div>El 04/10/2026, Ana escribió:</div><blockquote><p>Pedido</p></blockquote>',
    );
    expect(split?.main).toContain('Listo');
    expect(split?.quoted).toContain('Ana escribió:');
    expect(split?.quoted).toContain('Pedido');
  });

  it('el separador de Outlook va con la cita', () => {
    const split = splitQuotedHtml(
      '<p>Visto</p><hr><div id="divRplyFwdMsg"><b>De:</b> Luis</div><div>Original</div>',
    );
    expect(split?.main).toContain('Visto');
    expect(split?.main).not.toContain('<hr>');
    expect(split?.quoted).toContain('Original');
  });

  it('no pliega nada sin cita, si todo es cita o si hay texto despues de un blockquote', () => {
    expect(splitQuotedHtml('<p>Solo texto</p>')).toBeNull();
    expect(splitQuotedHtml('<blockquote>Todo cita</blockquote>')).toBeNull();
    expect(
      splitQuotedHtml('<p>Arriba</p><blockquote>Cita</blockquote><p>Respuesta</p>'),
    ).toBeNull();
    expect(splitQuotedHtml('')).toBeNull();
  });

  it('una imagen cuenta como contenido propio', () => {
    const split = splitQuotedHtml(
      '<img src="data:image/png;base64,AA"><blockquote>Cita</blockquote>',
    );
    expect(split?.main).toContain('<img');
  });
});

describe('historial citado en texto', () => {
  it('pliega el bloque final de lineas citadas con su atribucion', () => {
    const split = splitQuotedText('Gracias\n\nEl lun, Luis escribió:\n> Hola\n>\n> Saludos\n');
    expect(split).toEqual({
      main: 'Gracias',
      quoted: 'El lun, Luis escribió:\n> Hola\n>\n> Saludos',
    });
  });

  it('pliega desde el separador de mensaje original', () => {
    const split = splitQuotedText('Visto\n\n-----Mensaje original-----\nDe: Luis\nHola');
    expect(split?.main).toBe('Visto');
    expect(split?.quoted).toContain('De: Luis');
  });

  it('no pliega una respuesta intercalada ni un mensaje que es todo cita', () => {
    expect(splitQuotedText('> pregunta\nrespuesta')).toBeNull();
    expect(splitQuotedText('> solo cita')).toBeNull();
    expect(splitQuotedText('Sin cita')).toBeNull();
  });
});
