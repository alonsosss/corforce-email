import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import robots from '../../public/robots.txt?raw';
import nginx from '../../nginx.conf?raw';
import { paths } from '@/paths';

// La lista unica de rastreadores de IA (capa 1 de docs/Plan_Proteccion_Frente_a_Bots.md): el
// borde la convierte en 403 y robots.txt tiene que prohibirles todo, incluido lo publico.
const AI_CRAWLERS = readFileSync(
  resolve(__dirname, '../../../selfhosted/edge/ai-crawlers.txt'),
  'utf8',
)
  .split('\n')
  .map((line) => line.replace(/#.*/, '').trim())
  .filter(Boolean);

interface Group {
  agents: string[];
  rules: { allow: boolean; prefix: string }[];
}

// Grupos de robots.txt segun RFC 9309: lineas User-agent seguidas de sus reglas.
function groups(): Group[] {
  const out: Group[] = [];
  let current: Group | null = null;
  let lastWasAgent = false;
  for (const raw of robots.split('\n')) {
    const line = raw.replace(/#.*/, '').trim();
    const agent = /^user-agent:\s*(.+)$/i.exec(line);
    const rule = /^(allow|disallow):\s*(\S*)$/i.exec(line);
    if (agent) {
      if (!lastWasAgent || !current) {
        current = { agents: [], rules: [] };
        out.push(current);
      }
      current.agents.push(agent[1]!.trim().toLowerCase());
      lastWasAgent = true;
    } else if (rule && current) {
      current.rules.push({ allow: rule[1]!.toLowerCase() === 'allow', prefix: rule[2] ?? '' });
      lastWasAgent = false;
    }
  }
  return out;
}

function groupFor(agent: string): Group | undefined {
  const name = agent.toLowerCase();
  return (
    groups().find((g) => g.agents.includes(name)) ?? groups().find((g) => g.agents.includes('*'))
  );
}

// Evaluacion de un grupo: gana la regla de ruta mas larga; en empate, Allow.
function allowed(agent: string, path: string): boolean {
  const rules = (groupFor(agent)?.rules ?? []).filter(
    (rule) => rule.prefix !== '' && path.startsWith(rule.prefix),
  );
  if (rules.length === 0) return true;
  rules.sort((a, b) => b.prefix.length - a.prefix.length || Number(b.allow) - Number(a.allow));
  return rules[0]?.allow ?? true;
}

describe('robots.txt', () => {
  it('deja indexar a los buscadores solo las landing pages y las paginas publicas de citas', () => {
    expect(robots).toMatch(/^User-agent: \*$/m);
    for (const agent of ['*', 'Googlebot', 'Bingbot', 'Applebot']) {
      expect(allowed(agent, '/p/acme/oferta-verano'), agent).toBe(true);
      expect(allowed(agent, paths.publicBooking('c1', 'acme', 'ventas')), agent).toBe(true);
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
        expect(allowed(agent, path), `${agent} ${path}`).toBe(false);
      }
    }
  });

  it('prohibe todo a cada rastreador de IA de la lista unica, tambien lo publico', () => {
    expect(AI_CRAWLERS.length).toBeGreaterThan(10);
    for (const agent of [...AI_CRAWLERS, 'Google-Extended', 'Applebot-Extended']) {
      const group = groupFor(agent);
      expect(group?.agents.includes(agent.toLowerCase()), `${agent} sin grupo propio`).toBe(true);
      expect(group?.rules, agent).toEqual([{ allow: false, prefix: '/' }]);
      expect(allowed(agent, '/p/acme/oferta-verano'), agent).toBe(false);
    }
  });

  it('ningun buscador esta en la lista de rastreadores de IA', () => {
    const lower = AI_CRAWLERS.map((a) => a.toLowerCase());
    for (const searcher of ['googlebot', 'bingbot', 'duckduckbot', 'applebot']) {
      expect(lower, searcher).not.toContain(searcher);
    }
  });

  it('nginx lo sirve como fichero y no cae en la SPA', () => {
    expect(nginx).toMatch(/location = \/robots\.txt \{[^}]*try_files \$uri =404;/);
  });
});
