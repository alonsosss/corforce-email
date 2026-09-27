// Deteccion de navegador automatizado (docs/Plan_Proteccion_Frente_a_Bots.md, capa 2). Corre
// antes de montar la aplicacion, sin dependencias y sin red, sobre las senales que un navegador
// controlado por una herramienta (Playwright, Puppeteer, Selenium, el MCP de Chrome DevTools)
// deja y un navegador de una persona no. Ninguna senal de accesibilidad puntua: un lector de
// pantalla o un navegador antiguo no se bloquean.
//
// Limite honesto: las herramientas "sigilosas" borran estas senales. Esta capa atrapa la
// automatizacion corriente; las credenciales, el sondeo y las exportaciones las vigilan las capas
// 3 a 5, que no dependen del navegador.

/** Lo que el detector mira del navegador. Se inyecta para poder probarlo sin un navegador real. */
export interface AutomationEnvironment {
  webdriver: boolean | undefined;
  userAgent: string;
  languages: readonly string[] | undefined;
  pluginCount: number | undefined;
  outerWidth: number;
  outerHeight: number;
  /** Nombres de propiedad de window y document: las herramientas dejan los suyos. */
  globalKeys: readonly string[];
}

export type AutomationSignal =
  | 'webdriver'
  | 'headless_user_agent'
  | 'automation_globals'
  | 'zero_window'
  | 'no_languages'
  | 'no_plugins';

export interface AutomationVerdict {
  blocked: boolean;
  score: number;
  signals: AutomationSignal[];
}

/** Peso de cada senal. Una decisiva (3) basta sola; las debiles (1) solo suman entre varias. */
export const SIGNAL_WEIGHTS: Record<AutomationSignal, number> = {
  webdriver: 3,
  headless_user_agent: 3,
  automation_globals: 3,
  zero_window: 2,
  no_languages: 1,
  no_plugins: 1,
};

/** Puntuacion a partir de la cual no se monta la aplicacion. */
export const BLOCK_THRESHOLD = 3;

/**
 * Que se hace con un navegador detectado. enforce: pagina de bloqueo. observe: se comunica la
 * referencia igual, pero la aplicacion se monta; sirve para revisar la interfaz con un navegador
 * automatizado en desarrollo y para calibrar. Solo se lee de VITE_AUTOMATION_MODE al compilar: la
 * imagen de produccion no recibe esa variable, asi que compila en enforce.
 */
export type AutomationMode = 'enforce' | 'observe';

/** Cualquier valor que no sea exactamente "observe" es enforce: ante la duda, se bloquea. */
export function parseAutomationMode(raw: string | undefined | null): AutomationMode {
  return raw?.trim() === 'observe' ? 'observe' : 'enforce';
}

/**
 * Etiqueta que el gateway anade al documento mientras hay un pase de desarrollo abierto para la
 * IP del visitante (services/gateway/automation_pass.go). La web no la escribe nunca.
 */
export const AUTOMATION_MODE_META = 'cfm-automation-mode';

/** Modo con el que arranca la aplicacion: observe si lo pide la compilacion o el gateway. */
export function effectiveAutomationMode(
  buildValue: string | undefined,
  doc: Pick<Document, 'querySelector'>,
): AutomationMode {
  if (parseAutomationMode(buildValue) === 'observe') return 'observe';
  const meta = doc.querySelector(`meta[name="${AUTOMATION_MODE_META}"]`);
  return parseAutomationMode(meta?.getAttribute('content'));
}

const HEADLESS_UA = /HeadlessChrome|PhantomJS|SlimerJS|HtmlUnit/i;
// Rastros de chromedriver ($cdc_ y cdc_), Selenium, PhantomJS, Nightmare y WebKit automation.
const AUTOMATION_GLOBAL =
  /^(\$?cdc_|__webdriver|__driver_|__selenium|_Selenium_IDE|__fxdriver|callPhantom|_phantom|__nightmare|domAutomation|__lastWatirAlert|calledSelenium)/;
const CHROME_DESKTOP = /Chrome\/\d+/;
const MOBILE_UA = /Mobi|Android|iPhone|iPad/;

export function detectAutomation(env: AutomationEnvironment): AutomationVerdict {
  const signals: AutomationSignal[] = [];
  if (env.webdriver === true) signals.push('webdriver');
  if (HEADLESS_UA.test(env.userAgent)) signals.push('headless_user_agent');
  if (env.globalKeys.some((k) => AUTOMATION_GLOBAL.test(k))) signals.push('automation_globals');
  if (env.outerWidth === 0 && env.outerHeight === 0) signals.push('zero_window');
  if (env.languages !== undefined && env.languages.length === 0) signals.push('no_languages');
  if (
    env.pluginCount === 0 &&
    CHROME_DESKTOP.test(env.userAgent) &&
    !MOBILE_UA.test(env.userAgent)
  ) {
    signals.push('no_plugins');
  }
  const score = signals.reduce((sum, s) => sum + SIGNAL_WEIGHTS[s], 0);
  return { blocked: score >= BLOCK_THRESHOLD, score, signals };
}

/** Lee del navegador real lo que el detector necesita. Cualquier fallo cuenta como "sin dato". */
export function collectEnvironment(w: Window = window): AutomationEnvironment {
  const nav = w.navigator;
  const keys = (obj: object): string[] => {
    try {
      return Object.getOwnPropertyNames(obj);
    } catch {
      return [];
    }
  };
  return {
    webdriver: nav.webdriver,
    userAgent: nav.userAgent ?? '',
    languages: nav.languages,
    pluginCount: nav.plugins?.length,
    outerWidth: w.outerWidth,
    outerHeight: w.outerHeight,
    globalKeys: [...keys(w), ...keys(w.document)],
  };
}

const REFERENCE_ALPHABET = 'abcdefghijklmnopqrstuvwxyz0123456789';

/**
 * Referencia del bloqueo, como el "Reference Error" de las paginas de bloqueo comerciales: se
 * muestra a la persona y viaja al gateway, que la escribe en su registro. Asi un bloqueo
 * indebido se puede buscar y revisar. Aleatoria: no codifica nada.
 */
export function automationReference(random: (n: number) => Uint8Array = randomBytes): string {
  const bytes = random(12);
  let out = '';
  for (const b of bytes) out += REFERENCE_ALPHABET[b % REFERENCE_ALPHABET.length];
  return out;
}

function randomBytes(n: number): Uint8Array {
  const bytes = new Uint8Array(n);
  crypto.getRandomValues(bytes);
  return bytes;
}

export interface AutomationReport {
  reference: string;
  path: string;
  signals: AutomationSignal[];
}

/**
 * Comunica el bloqueo al gateway. Sin esperar la respuesta ni fallar por ella: la pagina de
 * bloqueo se muestra igual. keepalive para que la peticion sobreviva si la pestana se cierra.
 */
export function reportAutomation(
  endpoint: string,
  report: AutomationReport,
  send: typeof fetch = fetch,
): void {
  try {
    void send(endpoint, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(report),
      keepalive: true,
      credentials: 'omit',
    }).catch(() => undefined);
  } catch {
    // Sin red o sin fetch: el bloqueo no depende del aviso.
  }
}
