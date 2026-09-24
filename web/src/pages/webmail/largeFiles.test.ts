import { describe, expect, it } from 'vitest';
import type { LargeFile, LargeFileListing } from '@/api/largeFiles';
import {
  expiryOptions,
  insertLargeFileLink,
  largeFileLinkHtml,
  largeFileLinkText,
  largeFileProblem,
  largeFileStateTone,
} from './largeFiles';
import { signatureHtml, signatureText } from './richText';

const LISTING: LargeFileListing = {
  enabled: true,
  items: [],
  usage: { mailbox_bytes: 900, mailbox_active: 1, tenant_bytes: 900 },
  limits: {
    max_file_bytes: 500,
    default_expiry_days: 7,
    max_expiry_days: 30,
    default_max_downloads: 20,
    max_downloads: 100,
    mailbox_quota_bytes: 1000,
    tenant_quota_bytes: 5000,
    max_active_per_mailbox: 2,
  },
};

const FILE: LargeFile = {
  id: 'f1',
  name: 'planos <b>final</b>.dwg',
  size_bytes: 2048,
  sha256: 'a'.repeat(64),
  url: 'https://correo.example.com/api/v1/public/files/t/f?x=1&s=abc',
  state: 'active',
  expires_at: '2026-10-01T12:00:00Z',
  max_downloads: 20,
  downloads: 0,
  remaining_downloads: 20,
  created_at: '2026-09-24T12:00:00Z',
  last_download_at: null,
  revoked_at: null,
};

describe('ficheros grandes: reglas previas a la subida', () => {
  it('nombra el tope que se supera', () => {
    expect(largeFileProblem({ name: 'a.zip', size: 600 }, LISTING)).toMatch('a.zip');
    expect(largeFileProblem({ name: 'b.zip', size: 200 }, LISTING)).toMatch('100 B');
    expect(largeFileProblem({ name: 'c.zip', size: 100 }, LISTING)).toBeNull();
    const full = { ...LISTING, usage: { ...LISTING.usage, mailbox_active: 2 } };
    expect(largeFileProblem({ name: 'd.zip', size: 1 }, full)).toMatch('2');
  });

  it('ofrece plazos hasta el maximo con el por defecto incluido', () => {
    expect(expiryOptions(7, 30).map((o) => o.value)).toEqual(['1', '3', '7', '14', '30']);
    expect(expiryOptions(5, 10).map((o) => o.value)).toEqual(['1', '3', '5', '7', '10']);
    expect(expiryOptions(1, 1)).toHaveLength(1);
  });

  it('da un tono a cada estado', () => {
    expect(largeFileStateTone('active')).toBe('success');
    expect(largeFileStateTone('failed')).toBe('danger');
  });
});

describe('ficheros grandes: enlace en el cuerpo', () => {
  it('escribe el nombre como texto y el enlace como atributo', () => {
    const html = largeFileLinkHtml(FILE);
    const doc = new DOMParser().parseFromString(html, 'text/html');
    const a = doc.querySelector('a');
    expect(doc.querySelector('b')).toBeNull();
    expect(a?.textContent).toBe(FILE.name);
    expect(a?.getAttribute('href')).toBe(FILE.url);
    expect(largeFileLinkText(FILE)).toContain(FILE.url);
  });

  it('va antes de la firma, arriba si hay cita y al final si no', () => {
    const block = '<p>ENLACE</p>';
    const withSignature = `<div>hola</div>${signatureHtml('<p>Ana</p>')}`;
    const html = insertLargeFileLink(withSignature, block, 'html', false);
    expect(html.indexOf('ENLACE')).toBeLessThan(html.indexOf('-- '));
    expect(html.startsWith('<div>hola</div><p>ENLACE</p>')).toBe(true);

    expect(insertLargeFileLink('<blockquote>cita</blockquote>', block, 'html', true)).toBe(
      '<p>ENLACE</p><blockquote>cita</blockquote>',
    );
    expect(insertLargeFileLink('<div>hola</div>', block, 'html', false)).toBe(
      '<div>hola</div><p>ENLACE</p>',
    );

    const text = insertLargeFileLink(`hola${signatureText('Ana')}`, 'ENLACE\n', 'text', false);
    expect(text.indexOf('ENLACE')).toBeLessThan(text.indexOf('-- '));
    expect(insertLargeFileLink('hola', 'ENLACE\n', 'text', false)).toBe('hola\nENLACE\n');
  });
});
