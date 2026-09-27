import { describe, expect, it, vi } from 'vitest';
import {
  automationReference,
  BLOCK_THRESHOLD,
  detectAutomation,
  parseAutomationMode,
  reportAutomation,
  type AutomationEnvironment,
} from './automation';

const CHROME =
  'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36';

function human(over: Partial<AutomationEnvironment> = {}): AutomationEnvironment {
  return {
    webdriver: false,
    userAgent: CHROME,
    languages: ['es-PE', 'es'],
    pluginCount: 5,
    outerWidth: 1440,
    outerHeight: 900,
    globalKeys: ['window', 'document', 'localStorage', 'React', 'cf_theme'],
    ...over,
  };
}

describe('deteccion de navegador automatizado', () => {
  it('una persona con Chrome, Firefox, Safari o un movil no se bloquea', () => {
    for (const env of [
      human(),
      human({
        userAgent: 'Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0',
        pluginCount: 0,
      }),
      human({
        userAgent:
          'Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15',
        pluginCount: 0,
        webdriver: undefined,
      }),
      human({
        userAgent:
          'Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36',
        pluginCount: 0,
      }),
      // Un navegador viejo sin navigator.languages ni webdriver: sin datos no es sospechoso.
      human({ languages: undefined, webdriver: undefined, pluginCount: undefined }),
    ]) {
      const v = detectAutomation(env);
      expect(v.blocked, JSON.stringify(v)).toBe(false);
    }
  });

  it('navigator.webdriver (el MCP de Chrome DevTools, Playwright, Selenium) basta solo', () => {
    const v = detectAutomation(human({ webdriver: true }));
    expect(v).toEqual({ blocked: true, score: 3, signals: ['webdriver'] });
  });

  it('un Chrome sin cabeza o con rastros de chromedriver se bloquea aunque oculte webdriver', () => {
    expect(
      detectAutomation(
        human({
          userAgent: CHROME.replace('Chrome/130.0.0.0', 'HeadlessChrome/130.0.0.0'),
        }),
      ).signals,
    ).toContain('headless_user_agent');
    expect(
      detectAutomation(human({ globalKeys: ['window', '$cdc_asdjflasutopfhvcZLmcfl_'] })).signals,
    ).toContain('automation_globals');
    expect(detectAutomation(human({ globalKeys: ['__selenium_unwrapped'] })).blocked).toBe(true);
  });

  it('las senales debiles solo bloquean entre varias', () => {
    // Ventana 0x0 sola no llega al umbral.
    expect(detectAutomation(human({ outerWidth: 0, outerHeight: 0 })).blocked).toBe(false);
    // Ventana 0x0 mas Chrome de escritorio sin plugins ni idiomas: si.
    const v = detectAutomation(
      human({ outerWidth: 0, outerHeight: 0, pluginCount: 0, languages: [] }),
    );
    expect(v.score).toBeGreaterThanOrEqual(BLOCK_THRESHOLD);
    expect(v.signals).toEqual(['zero_window', 'no_languages', 'no_plugins']);
  });

  it('la referencia es aleatoria, corta y solo con letras y digitos', () => {
    const ref = automationReference((n) => new Uint8Array(n).map((_, i) => (i * 37) % 256));
    expect(ref).toMatch(/^[a-z0-9]{12}$/);
    expect(automationReference()).not.toBe(automationReference());
  });

  it('el aviso al gateway no bloquea ni falla aunque no haya red', () => {
    const send = vi.fn().mockRejectedValue(new Error('sin red'));
    expect(() =>
      reportAutomation(
        '/api/v1/public/security/automation-detected',
        { reference: 'abc123def456', path: '/login', signals: ['webdriver'] },
        send as unknown as typeof fetch,
      ),
    ).not.toThrow();
    expect(send).toHaveBeenCalledWith(
      '/api/v1/public/security/automation-detected',
      expect.objectContaining({
        method: 'POST',
        keepalive: true,
        credentials: 'omit',
        body: JSON.stringify({ reference: 'abc123def456', path: '/login', signals: ['webdriver'] }),
      }),
    );
  });

  it('el modo solo es observe cuando se pide exactamente asi; ante la duda, enforce', () => {
    expect(parseAutomationMode('observe')).toBe('observe');
    expect(parseAutomationMode(' observe ')).toBe('observe');
    for (const raw of [undefined, '', 'enforce', 'Observe', 'OBSERVE', 'off', 'false', '0']) {
      expect(parseAutomationMode(raw)).toBe('enforce');
    }
  });
});
