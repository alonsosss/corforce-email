import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useLocation, useNavigate, useSearchParams } from 'react-router-dom';
import { useToast } from '@/design/components';
import { t } from '@/i18n';
import { paths } from '@/paths';
import ComposePage from './ComposePage';
import {
  ComposePristineContext,
  ComposeWindowContext,
  composeBackdropPath,
  composeRequestFromUrl,
  useComposer,
  type ComposeController,
  type ComposeRequest,
  type ComposerSize,
} from './composeWindow';

interface OpenCompose {
  id: number;
  request: ComposeRequest;
}

/**
 * La ventana de redaccion vive en el marco del webmail, no en una ruta: sigue abierta mientras
 * el usuario busca, abre otros mensajes o pasa a contactos o al calendario. Una sola redaccion a
 * la vez; otra peticion solo la sustituye si la actual sigue intacta.
 */
export function useComposerHost(): { composer: ComposeController; composerWindow: ReactNode } {
  const toast = useToast();
  const location = useLocation();
  const [current, setCurrent] = useState<OpenCompose | null>(null);
  const [size, setSize] = useState<ComposerSize>('normal');
  const currentRef = useRef<OpenCompose | null>(null);
  const pristine = useRef(true);
  const nextId = useRef(0);
  const lastView = useRef<string | null>(null);

  useEffect(() => {
    if (location.pathname !== paths.webmailCompose) {
      lastView.current = `${location.pathname}${location.search}`;
    }
  }, [location.pathname, location.search]);

  const open = useCallback(
    (request: ComposeRequest) => {
      if (currentRef.current && !pristine.current) {
        setSize((value) => (value === 'minimized' ? 'normal' : value));
        toast.info(t('webmail.composer.oneAtATime'));
        return;
      }
      nextId.current += 1;
      const next = { id: nextId.current, request };
      currentRef.current = next;
      pristine.current = true;
      setCurrent(next);
      setSize('normal');
    },
    [toast],
  );

  const close = useCallback((id: number) => {
    if (currentRef.current?.id !== id) return;
    currentRef.current = null;
    setCurrent(null);
  }, []);

  const reportPristine = useCallback((value: boolean) => {
    pristine.current = value;
  }, []);

  const composer = useMemo<ComposeController>(
    () => ({ open, lastView: () => lastView.current }),
    [open],
  );

  const composerWindow = current ? (
    <ComposeWindow key={current.id} size={size} setSize={setSize}>
      <ComposePristineContext.Provider value={reportPristine}>
        <ComposePage request={current.request} onClose={() => close(current.id)} />
      </ComposePristineContext.Provider>
    </ComposeWindow>
  ) : null;

  return { composer, composerWindow };
}

function ComposeWindow({
  size,
  setSize,
  children,
}: {
  size: ComposerSize;
  setSize: (size: ComposerSize) => void;
  children: ReactNode;
}) {
  const value = useMemo(() => ({ size, setSize }), [size, setSize]);
  return (
    <ComposeWindowContext.Provider value={value}>
      {size === 'expanded' ? (
        <div
          className="cf-wm-composer__backdrop"
          aria-hidden="true"
          onClick={() => setSize('normal')}
        />
      ) : null}
      <section
        className={`cf-wm-composer cf-wm-composer--${size}`}
        aria-label={t('webmail.composer.label')}
        data-keyboard-owner={size === 'expanded' ? 'capture' : 'focus'}
      >
        {children}
      </section>
    </ComposeWindowContext.Provider>
  );
}

/**
 * Entrada por la ruta /webmail/compose: los enlaces de responder, reenviar, abrir un borrador o
 * escribir a un contacto abren la ventana del marco con su peticion y dejan debajo el mensaje de
 * origen o la vista en la que estaba el usuario. El estado de la navegacion (el texto propuesto
 * por el asistente) se consume aqui una sola vez.
 */
export default function ComposeRoute() {
  const [params] = useSearchParams();
  const location = useLocation();
  const navigate = useNavigate();
  const toast = useToast();
  const composer = useComposer();
  const handled = useRef<string | null>(null);

  useEffect(() => {
    if (handled.current === location.key) return;
    handled.current = location.key;
    const request = composeRequestFromUrl(params, location.state);
    if (!request) {
      toast.error(t('webmail.compose.invalidLink'));
      navigate(composer.lastView() ?? paths.webmail, { replace: true });
      return;
    }
    composer.open(request);
    navigate(composeBackdropPath(request, composer.lastView()), { replace: true });
  }, [location.key, location.state, params, navigate, toast, composer]);

  return null;
}
