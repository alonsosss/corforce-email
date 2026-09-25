import { useMemo, useState, type ReactNode } from 'react';
import { t } from '@/i18n';
import ComposePage from './ComposePage';
import MailboxPage from './MailboxPage';
import { ComposeWindowContext, type ComposerSize } from './composeWindow';

/**
 * La redaccion flota sobre el buzon, como en los webmails del mercado. La ruta sigue siendo
 * /webmail/compose: los enlaces de responder, reenviar o abrir un borrador no cambian, y el
 * buzon de fondo muestra la carpeta y el mensaje de origen que lleva la misma URL.
 */
export default function ComposeRoute() {
  return (
    <>
      <MailboxPage />
      <ComposeWindow>
        <ComposePage />
      </ComposeWindow>
    </>
  );
}

function ComposeWindow({ children }: { children: ReactNode }) {
  const [size, setSize] = useState<ComposerSize>('normal');
  const value = useMemo(() => ({ size, setSize }), [size]);
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
      >
        {children}
      </section>
    </ComposeWindowContext.Provider>
  );
}
