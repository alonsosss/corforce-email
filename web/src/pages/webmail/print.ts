import type { MailMessage } from '@/api/webmail';
import { formatDateTime } from '@/lib/format';
import { prepareUntrustedHtml } from '@/lib/untrustedHtml';
import { t } from '@/i18n';
import { htmlToPlainText } from './compose';
import { addressList } from './format';

/*
 * Impresion de un mensaje. El cuerpo es HTML de fuera: nunca entra en el documento de la
 * aplicacion. Se imprime desde un iframe oculto con sandbox sin scripts; allow-same-origin
 * solo deja a la aplicacion llamar a print() del marco (sin allow-scripts el HTML no puede
 * aprovecharlo) y allow-modals permite el dialogo de impresion.
 */

const PRINT_SANDBOX = 'allow-same-origin allow-modals';
const REMOVE_AFTER_MS = 60_000;

const PRINT_CSS = `
  body { font-family: system-ui, sans-serif; font-size: 12pt; color: #000; background: #fff; margin: 0; }
  .cf-print-head { border-bottom: 1px solid #999; padding-bottom: 8pt; margin-bottom: 12pt; }
  .cf-print-head h1 { font-size: 16pt; margin: 0 0 8pt; }
  .cf-print-head table { border-collapse: collapse; font-size: 10pt; }
  .cf-print-head th { text-align: left; padding: 1pt 8pt 1pt 0; vertical-align: top; }
  .cf-print-text { white-space: pre-wrap; font-family: inherit; margin: 0; }
  img { max-width: 100%; }
  @page { margin: 15mm; }
`;

function escapeHtml(text: string): string {
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

export interface PrintOptions {
  /** Quien lee ya pidio las imagenes remotas de este mensaje. */
  allowRemoteImages: boolean;
  /** Imagenes en linea ya descargadas para la lectura: URL de la parte -> data:. */
  inlineImages?: ReadonlyMap<string, string>;
}

/** Documento de impresion: cabeceras escapadas y el cuerpo preparado como en la lectura. */
export function buildPrintDocument(message: MailMessage, options: PrintOptions): string {
  const subject = message.subject || t('webmail.noSubject');
  const rows: [string, string][] = [
    [t('webmail.header.from'), addressList(message.from)],
    [t('webmail.header.to'), addressList(message.to)],
  ];
  if (message.cc.length) rows.push([t('webmail.header.cc'), addressList(message.cc)]);
  rows.push([t('webmail.header.date'), formatDateTime(message.date)]);
  const head = `<header class="cf-print-head"><h1>${escapeHtml(subject)}</h1><table>${rows
    .map(([label, value]) => `<tr><th>${escapeHtml(label)}</th><td>${escapeHtml(value)}</td></tr>`)
    .join('')}</table></header>`;
  const body = message.html.trim()
    ? message.html
    : `<pre class="cf-print-text">${escapeHtml(message.text || htmlToPlainText(message.html))}</pre>`;
  const source = `<!DOCTYPE html><html><head><meta charset="utf-8"><title>${escapeHtml(
    subject,
  )}</title><style>${PRINT_CSS}</style></head><body>${head}${body}</body></html>`;
  return prepareUntrustedHtml(source, {
    allowRemoteImages: options.allowRemoteImages && !message.remote_images.blocked,
    inlineImages: options.inlineImages,
  }).html;
}

/** Abre el dialogo de impresion del navegador con el mensaje; devuelve el marco creado. */
export function printMessage(message: MailMessage, options: PrintOptions): HTMLIFrameElement {
  document.querySelectorAll('iframe[data-cf-print]').forEach((old) => old.remove());
  const frame = document.createElement('iframe');
  frame.setAttribute('data-cf-print', '');
  frame.setAttribute('sandbox', PRINT_SANDBOX);
  frame.setAttribute('referrerpolicy', 'no-referrer');
  frame.setAttribute('aria-hidden', 'true');
  frame.setAttribute('tabindex', '-1');
  frame.title = t('webmail.reader.print');
  frame.className = 'cf-wm-print-frame';
  frame.addEventListener('load', () => {
    const view = frame.contentWindow;
    if (!view) return;
    view.focus();
    view.print();
    window.setTimeout(() => frame.remove(), REMOVE_AFTER_MS);
  });
  frame.srcdoc = buildPrintDocument(message, options);
  document.body.appendChild(frame);
  return frame;
}
