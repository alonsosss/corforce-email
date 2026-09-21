import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as webmailModule from '@/api/webmail';
import {
  DEBOUNCE_MS,
  POLL_INTERVAL_MS,
  REOPEN_AFTER_MS,
  VISIBILITY_THROTTLE_MS,
  watchInbox,
} from './events';

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  readyState = 0;
  onerror: (() => void) | null = null;
  closed = false;
  private listeners = new Map<string, (() => void)[]>();
  constructor() {
    FakeEventSource.instances.push(this);
  }
  addEventListener(type: string, fn: () => void) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), fn]);
  }
  close() {
    this.closed = true;
    this.readyState = 2;
  }
  emit(type: string) {
    for (const fn of this.listeners.get(type) ?? []) fn();
  }
  /** El navegador esta reconectando por su cuenta. */
  reconnecting() {
    this.readyState = 0;
    this.onerror?.();
  }
  /** El servicio rechazo la conexion: no se reconecta solo. */
  rejected() {
    this.readyState = 2;
    this.onerror?.();
  }
}

function setVisibility(state: 'visible' | 'hidden') {
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => state });
}

describe('avisos de la bandeja', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    FakeEventSource.instances = [];
    vi.stubGlobal('EventSource', FakeEventSource);
    vi.spyOn(webmailModule, 'openWebmailEvents').mockImplementation(
      () => new FakeEventSource() as unknown as EventSource,
    );
    setVisibility('visible');
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  const start = () => {
    const onChange = vi.fn();
    const onSessionExpired = vi.fn();
    const stop = watchInbox({ onChange, onSessionExpired });
    return {
      onChange,
      onSessionExpired,
      stop,
      es: () => FakeEventSource.instances[FakeEventSource.instances.length - 1]!,
    };
  };

  it('un aviso de la bandeja vuelve a leer, y varios seguidos se funden en una sola lectura', () => {
    const { onChange, es, stop } = start();
    es().emit('ready');
    es().emit('mailbox');
    es().emit('mailbox');
    es().emit('mailbox');
    expect(onChange).not.toHaveBeenCalled();
    vi.advanceTimersByTime(DEBOUNCE_MS);
    expect(onChange).toHaveBeenCalledTimes(1);
    stop();
  });

  it('sin flujo no sondea si la pestana esta oculta y sondea si esta visible', () => {
    const { onChange, es, stop } = start();
    es().reconnecting();
    setVisibility('hidden');
    vi.advanceTimersByTime(POLL_INTERVAL_MS * 2);
    expect(onChange).not.toHaveBeenCalled();
    setVisibility('visible');
    vi.advanceTimersByTime(POLL_INTERVAL_MS);
    expect(onChange).toHaveBeenCalledTimes(1);
    stop();
  });

  it('con el flujo abierto no sondea', () => {
    const { onChange, es, stop } = start();
    es().reconnecting();
    es().emit('ready');
    vi.advanceTimersByTime(POLL_INTERVAL_MS * 3);
    expect(onChange).not.toHaveBeenCalled();
    stop();
  });

  it('al reabrirse el flujo tras un corte vuelve a leer, porque los avisos perdidos no se repiten', () => {
    const { onChange, es, stop } = start();
    es().emit('ready');
    es().reconnecting();
    es().emit('ready');
    vi.advanceTimersByTime(DEBOUNCE_MS);
    expect(onChange).toHaveBeenCalledTimes(1);
    stop();
  });

  it('una sesion caida cierra el flujo, avisa y deja de sondear', () => {
    const { onChange, onSessionExpired, es, stop } = start();
    es().emit('ready');
    es().emit('session-expired');
    expect(onSessionExpired).toHaveBeenCalledTimes(1);
    expect(es().closed).toBe(true);
    vi.advanceTimersByTime(POLL_INTERVAL_MS * 3);
    expect(onChange).not.toHaveBeenCalled();
    stop();
  });

  it('un flujo rechazado para siempre se sustituye por sondeo y se reintenta mas tarde', () => {
    const { onChange, es, stop } = start();
    const first = es();
    first.rejected();
    expect(first.closed).toBe(true);
    vi.advanceTimersByTime(POLL_INTERVAL_MS);
    expect(onChange).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(REOPEN_AFTER_MS);
    expect(FakeEventSource.instances.length).toBeGreaterThan(1);
    stop();
  });

  it('al volver a la pestana sin flujo lee una vez, y no mas de una vez por margen', () => {
    const { onChange, es, stop } = start();
    es().reconnecting();
    vi.advanceTimersByTime(VISIBILITY_THROTTLE_MS + 1);
    document.dispatchEvent(new Event('visibilitychange'));
    document.dispatchEvent(new Event('visibilitychange'));
    expect(onChange).toHaveBeenCalledTimes(1);
    stop();
  });

  it('con el flujo abierto volver a la pestana no lee: el flujo ya avisa', () => {
    const { onChange, es, stop } = start();
    es().emit('ready');
    vi.advanceTimersByTime(VISIBILITY_THROTTLE_MS + 1);
    document.dispatchEvent(new Event('visibilitychange'));
    expect(onChange).not.toHaveBeenCalled();
    stop();
  });

  it('detener cierra el flujo y no deja temporizadores ni escuchas', () => {
    const { onChange, es, stop } = start();
    es().emit('ready');
    es().emit('mailbox');
    stop();
    expect(es().closed).toBe(true);
    vi.advanceTimersByTime(POLL_INTERVAL_MS * 3);
    document.dispatchEvent(new Event('visibilitychange'));
    expect(onChange).not.toHaveBeenCalled();
  });

  it('un navegador sin EventSource solo sondea', () => {
    vi.stubGlobal('EventSource', undefined);
    const { onChange, stop } = start();
    expect(FakeEventSource.instances).toHaveLength(0);
    vi.advanceTimersByTime(POLL_INTERVAL_MS);
    expect(onChange).toHaveBeenCalledTimes(1);
    stop();
  });
});
