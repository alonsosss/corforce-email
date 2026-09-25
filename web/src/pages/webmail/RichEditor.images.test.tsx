import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { t } from '@/i18n';
import { formatBytes } from '@/lib/quota';
import { RichEditor } from './RichEditor';
import { embedInlineImages } from './inlineImages';

const PNG = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);

function paste(files: File[]) {
  fireEvent.paste(screen.getByRole('textbox'), {
    clipboardData: { files, getData: () => '' },
  });
}

function renderEditor(maxImageBytes: number | null, onChange = vi.fn()) {
  render(
    <>
      <span id="lbl">Mensaje</span>
      <RichEditor
        id="body"
        labelledBy="lbl"
        initialHtml=""
        allowImages
        maxImageBytes={maxImageBytes}
        onChange={onChange}
      />
    </>,
  );
  return onChange;
}

describe('imagenes en el editor', () => {
  it('pegar una imagen la inserta incrustada', async () => {
    renderEditor(1024);
    paste([new File([PNG], 'captura.png', { type: 'image/png' })]);
    await waitFor(() =>
      expect(screen.getByRole('textbox').querySelector('img')?.getAttribute('src')).toMatch(
        /^data:image\/png;base64,/,
      ),
    );
    expect(screen.getByRole('button', { name: t('webmail.editor.image') })).toBeInTheDocument();
  });

  it('rechaza con aviso lo que no es un mapa de bits o supera el tope', async () => {
    renderEditor(4);
    paste([new File(['<svg/>'], 'logo.svg', { type: 'image/svg+xml' })]);
    expect(
      await screen.findByText(t('webmail.editor.imageType', { name: 'logo.svg' })),
    ).toBeInTheDocument();
    paste([new File([PNG], 'grande.png', { type: 'image/png' })]);
    expect(
      await screen.findByText(
        t('webmail.editor.imageTooLarge', { name: 'grande.png', max: formatBytes(4) }),
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole('textbox').querySelector('img')).toBeNull();
  });

  it('sin tope servido no ofrece insertar imagenes', () => {
    renderEditor(null);
    expect(screen.queryByRole('button', { name: t('webmail.editor.image') })).toBeNull();
  });
});

describe('borradores con imagenes', () => {
  it('las URL de las partes se cambian por las imagenes descargadas', () => {
    const url = '/api/v1/webmail/folders/Drafts/messages/4/parts/2';
    const html = `<p>Hola</p><img src="${url}"><img src="https://x.test/a.png">`;
    const out = embedInlineImages(html, new Map([[url, 'data:image/png;base64,AAAA']]));
    expect(out).toContain('src="data:image/png;base64,AAAA"');
    expect(out).toContain('src="https://x.test/a.png"');
    expect(out).not.toContain(url);
  });
});
