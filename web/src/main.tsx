import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './App';
import '@fontsource-variable/inter';
import '@/design/tokens.css';
import '@/design/base.css';
import '@/design/components.css';
import { endpoints } from '@/api/endpoints';
import { registerServiceWorker } from '@/pwa/registerServiceWorker';
import { AutomationBlockedPage } from '@/security/AutomationBlockedPage';
import {
  automationReference,
  collectEnvironment,
  detectAutomation,
  effectiveAutomationMode,
  reportAutomation,
} from '@/security/automation';

const container = document.getElementById('root');
if (!container) throw new Error('No existe el contenedor #root');

// Antes de montar nada: un navegador automatizado no ve la aplicacion ni registra el service
// worker (docs/Plan_Proteccion_Frente_a_Bots.md, capa 2). En observe (compilacion de desarrollo o
// pase de desarrollo del gateway) se comunica la referencia igual y la aplicacion se monta.
const verdict = detectAutomation(collectEnvironment());
const mode = effectiveAutomationMode(import.meta.env.VITE_AUTOMATION_MODE, document);
const reference = verdict.blocked ? automationReference() : null;
if (reference) {
  reportAutomation(endpoints.publicSecurity.automationDetected, {
    reference,
    path: window.location.pathname,
    signals: verdict.signals,
  });
  if (mode === 'observe') {
    console.warn(
      `Navegador automatizado detectado (${verdict.signals.join(', ')}); referencia ${reference}. ` +
        'Modo observe: la aplicacion se monta igual. Sin pase de desarrollo se bloquea.',
    );
  }
}
if (reference && mode === 'enforce') {
  createRoot(container).render(
    <StrictMode>
      <AutomationBlockedPage reference={reference} />
    </StrictMode>,
  );
} else {
  createRoot(container).render(
    <StrictMode>
      <App />
    </StrictMode>,
  );
  registerServiceWorker();
}
