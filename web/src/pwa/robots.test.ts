import { describe, expect, it } from 'vitest';
import robots from '../../public/robots.txt?raw';
import nginx from '../../nginx.conf?raw';
import { paths } from '@/paths';

// Evaluacion de robots.txt segun RFC 9309: gana la regla de ruta mas larga; en empate, Allow.
function allowed(path: string): boolean {
  const rules = robots
    .split('\n')
    .map((line) => line.replace(/#.*/, '').trim())
    .map((line) => /^(allow|disallow):\s*(\S*)$/i.exec(line))
    .filter((m): m is RegExpExecArray => m !== null)
    .map((m) => ({ allow: m[1]?.toLowerCase() === 'allow', prefix: m[2] ?? '' }))
    .filter((rule) => rule.prefix !== '' && path.startsWith(rule.prefix));
  if (rules.length === 0) return true;
  rules.sort((a, b) => b.prefix.length - a.prefix.length || Number(b.allow) - Number(a.allow));
  return rules[0]?.allow ?? true;
}

describe('robots.txt', () => {
  it('deja indexar solo las landing pages y las paginas publicas de citas', () => {
    expect(robots).toMatch(/^User-agent: \*$/m);
    expect(allowed('/p/acme/oferta-verano')).toBe(true);
    expect(allowed(paths.publicBooking('c1', 'acme', 'ventas'))).toBe(true);

    for (const path of [
      '/',
      paths.login,
      paths.webmail,
      paths.webmailLogin,
      paths.webmailCalendar,
      paths.mailboxes,
      '/api/v1/auth/login',
      '/public/files/acme/f1',
    ]) {
      expect(allowed(path), path).toBe(false);
    }
  });

  it('nginx lo sirve como fichero y no cae en la SPA', () => {
    expect(nginx).toMatch(/location = \/robots\.txt \{[^}]*try_files \$uri =404;/);
  });
});
