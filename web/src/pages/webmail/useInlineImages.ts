import { useMemo } from 'react';
import type { MailMessage } from '@/api/webmail';
import { useQuery } from '@/hooks/useQuery';
import { loadInlineImages, referencedInlineParts } from './inlineImages';

const NONE: ReadonlyMap<string, string> = new Map();

/**
 * Imagenes en linea de un mensaje ya abierto, descargadas una vez con la sesion del buzon: las
 * comparten la lectura y la impresion. Mientras llegan, el mapa esta vacio.
 */
export function useInlineImages(message: MailMessage | null): ReadonlyMap<string, string> {
  const refs = useMemo(() => (message ? referencedInlineParts(message) : null), [message]);
  const images = useQuery(
    (signal) =>
      message && refs?.size
        ? loadInlineImages(message, refs, signal)
        : Promise.resolve(new Map<string, string>()),
    [refs],
  );
  return images.data ?? NONE;
}
