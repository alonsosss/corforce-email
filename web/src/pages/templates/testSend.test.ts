import { describe, expect, it } from 'vitest';
import type { SendingDomain } from '@/api/transactional';
import { senderAddress, sendableDomains, testVariables } from './testSend';

function domain(name: string, canSend: boolean): SendingDomain {
  return {
    domain: name,
    status: canSend ? 'verified' : 'pending',
    purpose: 'both',
    sending_ready: canSend,
    can_send: canSend,
    updated_at: '2026-09-23T10:00:00Z',
  };
}

describe('sendableDomains', () => {
  it('ofrece solo los dominios desde los que transactional deja enviar, ordenados', () => {
    expect(
      sendableDomains([
        domain('zeta.test', true),
        domain('pendiente.test', false),
        domain('acme.test', true),
      ]),
    ).toEqual(['acme.test', 'zeta.test']);
  });
});

describe('senderAddress', () => {
  it('une la parte local escrita con el dominio elegido', () => {
    expect(senderAddress('  hola ', 'acme.test')).toBe('hola@acme.test');
  });
  it('sin parte local, sin dominio o con una @ escrita no hay remitente', () => {
    expect(senderAddress(' ', 'acme.test')).toBeNull();
    expect(senderAddress('hola', '')).toBeNull();
    expect(senderAddress('hola@otro.test', 'acme.test')).toBeNull();
  });
});

describe('testVariables', () => {
  it('una variable vacia no viaja, aunque sea requerida: el servicio pone un valor de ejemplo', () => {
    const out = testVariables(
      [
        { name: 'first_name', type: 'string', required: true },
        { name: 'total', type: 'number', required: false },
      ],
      { first_name: '', total: '12.5' },
    );
    expect(out.errors).toEqual({});
    expect(out.variables).toEqual({ total: 12.5 });
  });
  it('lo escrito se valida contra su tipo', () => {
    const out = testVariables([{ name: 'link', type: 'url', required: false }], {
      link: 'no-es-url',
    });
    expect(Object.keys(out.errors)).toEqual(['link']);
  });
});
