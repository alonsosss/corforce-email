import { createContext, useContext } from 'react';
import { paths } from '@/paths';
import { assistantTextFromState } from './assistant/assistant';
import { parseComposeMode, type ComposeMode } from './compose';
import { parsePositiveInt } from './format';

export type ComposerSize = 'normal' | 'minimized' | 'expanded';

export interface ComposeWindowState {
  size: ComposerSize;
  setSize: (size: ComposerSize) => void;
}

export const ComposeWindowContext = createContext<ComposeWindowState | null>(null);

/** Estado de la ventana flotante; null cuando la redaccion se monta sola (pruebas). */
export function useComposeWindow(): ComposeWindowState | null {
  return useContext(ComposeWindowContext);
}

/** Que se redacta: un mensaje nuevo (con destinatario opcional) o uno que parte de otro del buzon. */
export type ComposeRequest =
  | { kind: 'new'; to: string | null; assistantText: string | null }
  | {
      kind: 'source';
      mode: ComposeMode;
      folder: string;
      uid: number;
      assistantText: string | null;
    };

export const NEW_MESSAGE: ComposeRequest = { kind: 'new', to: null, assistantText: null };

/**
 * Peticion que lleva la ruta /webmail/compose (la query y el estado de la navegacion). null
 * cuando el enlace no es valido: un modo desconocido o un modo sin su mensaje de origen.
 */
export function composeRequestFromUrl(
  params: URLSearchParams,
  state: unknown,
): ComposeRequest | null {
  const rawMode = params.get('mode');
  const assistantText = assistantTextFromState(state);
  if (!rawMode) return { kind: 'new', to: params.get('to'), assistantText };
  const mode = parseComposeMode(rawMode);
  const folder = params.get('folder');
  const uid = parsePositiveInt(params.get('uid'));
  if (!mode || !folder || !uid) return null;
  return { kind: 'source', mode, folder, uid, assistantText };
}

/**
 * Vista que queda debajo de la ventana al abrirla por la ruta: el mensaje de origen (la carpeta,
 * si era un borrador que desaparece al enviarse) o la vista en la que estaba el usuario.
 */
export function composeBackdropPath(request: ComposeRequest, lastView: string | null): string {
  if (request.kind === 'new') return lastView ?? paths.webmail;
  if (request.mode === 'draft') return paths.webmailView({ folder: request.folder });
  const origin = paths.webmailView({ folder: request.folder, uid: request.uid });
  if (lastView?.startsWith(`${paths.webmail}?`)) {
    const params = new URLSearchParams(lastView.slice(paths.webmail.length + 1));
    if (params.get('folder') === request.folder && params.get('uid') === String(request.uid)) {
      return lastView;
    }
  }
  return origin;
}

/** Lo que el marco del webmail ofrece para redactar desde cualquier pantalla. */
export interface ComposeController {
  /** Abre la ventana; con otra redaccion en curso la trae al frente en lugar de sustituirla. */
  open: (request: ComposeRequest) => void;
  /** Ultima vista del webmail distinta de la ruta de redaccion (ruta y query), si la hay. */
  lastView: () => string | null;
}

export const ComposeControllerContext = createContext<ComposeController | null>(null);

export function useComposer(): ComposeController {
  const controller = useContext(ComposeControllerContext);
  if (!controller) throw new Error('useComposer fuera del marco del webmail');
  return controller;
}

/** La redaccion informa si sigue intacta: solo entonces otra peticion puede sustituirla. */
export const ComposePristineContext = createContext<((pristine: boolean) => void) | null>(null);
