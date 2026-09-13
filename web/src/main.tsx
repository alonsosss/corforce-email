import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './App';
import '@/design/tokens.css';
import '@/design/base.css';
import '@/design/components.css';

const container = document.getElementById('root');
if (!container) throw new Error('No existe el contenedor #root');

createRoot(container).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
