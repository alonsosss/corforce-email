import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './App';
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
  reportAutomation,
} from '@/security/automation';

const container = document.getElementById('root');
if (!container) throw new Error('No existe el contenedor #root');

// Antes de montar nada: un navegador automatizado no ve la aplicacion ni registra el service
// worker (docs/Plan_Proteccion_Frente_a_Bots.md, capa 2).
const verdict = detectAutomation(collectEnvironment());
if (verdict.blocked) {
  const reference = automationReference();
  reportAutomation(endpoints.publicSecurity.automationDetected, {
    reference,
    path: window.location.pathname,
    signals: verdict.signals,
  });
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
