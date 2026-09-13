import { endpoints } from '@/api/endpoints';
import { webmailApi, type MailMessage, type MessagePart } from '@/api/webmail';

/*
 * Imagenes en linea (cid:) del correo. El servicio reescribe cada cid: a la URL de su parte
 * en el API, pero el HTML se pinta en un iframe aislado (origen opaco) desde el que esa
 * peticion no llevaria la cookie del webmail. Aqui se piden las partes con la sesion y se
 * incrustan como data: de mapa de bits, lo unico que admite el marco.
 */

/**
 * Tipos que se incrustan: los mapas de bits que el servicio entrega con su tipo propio
 * (domain.SafeDownloadType) y que su saneado acepta como data:. SVG queda fuera: es un
 * documento con su propio DOM.
 */
const INLINE_IMAGE_TYPES = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp']);

/**
 * Partes en linea que el HTML referencia, por la URL exacta del src. La ruta de la carpeta
 * la escapa el servicio a su manera; se reconoce por el prefijo del API y el final
 * /messages/<uid>/parts/<parte>, que solo lleva digitos y puntos.
 */
export function referencedInlineParts(
  message: Pick<MailMessage, 'uid' | 'html' | 'attachments'>,
): Map<string, MessagePart> {
  const refs = new Map<string, MessagePart>();
  if (!message.html) return refs;
  const prefix = `${endpoints.webmail.folders}/`;
  const doc = new DOMParser().parseFromString(message.html, 'text/html');
  doc.querySelectorAll('img[src]').forEach((img) => {
    const src = (img.getAttribute('src') ?? '').trim();
    if (!src.startsWith(prefix) || refs.has(src)) return;
    const part = message.attachments.find(
      (p) => p.content_id && src.endsWith(`/messages/${message.uid}/parts/${p.part}`),
    );
    if (part) refs.set(src, part);
  });
  return refs;
}

export function blobToDataUrl(blob: Blob, type: string): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(reader.error ?? new Error('FileReader'));
    reader.readAsDataURL(new Blob([blob], { type }));
  });
}

/**
 * Descarga las partes referenciadas y devuelve URL del HTML -> data:. Una parte que no se
 * puede pedir o que no es un mapa de bits simplemente no se muestra.
 */
export async function loadInlineImages(
  message: Pick<MailMessage, 'folder' | 'uid'>,
  refs: ReadonlyMap<string, MessagePart>,
  signal?: AbortSignal,
): Promise<Map<string, string>> {
  const out = new Map<string, string>();
  await Promise.all(
    [...refs].map(async ([src, part]) => {
      try {
        const { blob, contentType } = await webmailApi.downloadPart(
          message.folder,
          message.uid,
          part.part,
          signal,
        );
        if (!INLINE_IMAGE_TYPES.has(contentType)) return;
        out.set(src, await blobToDataUrl(blob, contentType));
      } catch {
        // Sin la imagen el mensaje se sigue leyendo; un SESSION_EXPIRED ya lo trata el cliente.
      }
    }),
  );
  return out;
}
