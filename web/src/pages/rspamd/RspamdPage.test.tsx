import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { ApiError } from '@/api/errors';
import {
  mailSecurityApi,
  RSPAMD_HISTORY_MAX_ROWS,
  type RspamdHistory,
  type RspamdStats,
} from '@/api/mailSecurity';
import { t } from '@/i18n';
import RspamdPage from './RspamdPage';

const STATS: RspamdStats = {
  version: '3.11.1',
  uptime_seconds: 7300,
  scanned: 1234,
  learned: 7,
  spam_count: 34,
  ham_count: 1200,
  actions: { 'no action': 1200, reject: 30, 'add header': 4 },
  connections: 500,
  control_connections: 9,
  total_learns: 7,
  statfiles: [
    { symbol: 'BAYES_SPAM', type: 'redis', revision: 7, used: 5, total: 9, size: 1024, languages: 0, users: 1 },
    { symbol: 'BAYES_HAM', type: 'redis', revision: 7, used: 2, total: 9, size: 1024, languages: 0, users: 1 },
  ],
  fuzzy_hashes: { 'rspamd.com': 123456 },
  scan_time: { samples: 3, average_ms: '160', max_ms: '300' },
};

const HISTORY: RspamdHistory = {
  total: 3,
  truncated: true,
  rows: [
    {
      id: 'AAA1',
      time: '2026-09-21T12:00:00Z',
      ip: '203.0.113.9',
      user: 'ana@acme.test',
      sender: 'ana@acme.test',
      recipients: ['bea@acme.test', 'ceo@acme.test'],
      subject: 'Informe <b>x</b>',
      score: '1.5',
      required_score: '15',
      action: 'no action',
      symbols: [
        { name: 'DKIM_ALLOW', score: '-0.2' },
        { name: 'MIME_GOOD', score: '-0.1' },
      ],
      size: 2048,
      scan_time_ms: '42.1',
      skipped: false,
    },
    {
      id: 'BBB2',
      time: '2026-09-21T11:59:00Z',
      ip: '198.51.100.7',
      sender: 'spam@ejemplo.org',
      recipients: ['ana@acme.test'],
      subject: '',
      score: '22.4',
      required_score: '15',
      action: 'reject',
      symbols: [],
      size: 9,
      scan_time_ms: '900',
      skipped: false,
    },
  ],
};

function renderPage(path = '/platform/rspamd') {
  render(
    <MemoryRouter initialEntries={[path]}>
      <RspamdPage />
    </MemoryRouter>,
  );
}

describe('antispam de la celda (superadmin)', () => {
  afterEach(() => vi.restoreAllMocks());

  it('muestra los contadores, los veredictos y el clasificador bayesiano', async () => {
    vi.spyOn(mailSecurityApi, 'rspamdStats').mockResolvedValue(STATS);
    const history = vi.spyOn(mailSecurityApi, 'rspamdHistory').mockResolvedValue(HISTORY);
    renderPage();

    expect(await screen.findByText('3.11.1')).toBeInTheDocument();
    expect(screen.getByText(new Intl.NumberFormat().format(1234))).toBeInTheDocument();
    expect(screen.getByText(t('rspamd.stat.hours', { n: 2 }))).toBeInTheDocument();
    expect(
      screen.getByText(t('rspamd.stat.scanTimeValue', { avg: '160', max: '300', n: 3 })),
    ).toBeInTheDocument();
    expect(screen.getByText('reject')).toBeInTheDocument();
    expect(screen.getByText('BAYES_SPAM')).toBeInTheDocument();
    expect(screen.getByText(/rspamd\.com: /)).toBeInTheDocument();
    // La pestana de estadisticas no pide el historial.
    expect(history).not.toHaveBeenCalled();
  });

  it('el historial pide el tope del servicio y muestra sobre y veredicto como texto', async () => {
    const user = userEvent.setup();
    vi.spyOn(mailSecurityApi, 'rspamdStats').mockResolvedValue(STATS);
    const history = vi.spyOn(mailSecurityApi, 'rspamdHistory').mockResolvedValue(HISTORY);
    renderPage();
    await screen.findByText('3.11.1');

    await user.click(screen.getByRole('tab', { name: t('rspamd.tab.history') }));
    expect(await screen.findByText('spam@ejemplo.org')).toBeInTheDocument();
    expect(history).toHaveBeenCalledWith(RSPAMD_HISTORY_MAX_ROWS, expect.anything());
    expect(screen.getByText(/bea@acme.test/)).toBeInTheDocument();
    expect(
      screen.getByText(t('rspamd.history.moreRecipients', { n: 1 }), { exact: false }),
    ).toBeInTheDocument();
    // El asunto se pinta como texto, nunca como HTML.
    const subject = screen.getByText('Informe <b>x</b>');
    expect(subject.querySelector('b')).toBeNull();
    expect(screen.getByText(t('rspamd.history.noSubject'))).toBeInTheDocument();
    const symbols = screen.getByLabelText(t('rspamd.history.symbolsFor', { id: 'AAA1' }));
    expect(within(symbols).getByText(/DKIM_ALLOW\(-0\.2\)/)).toBeInTheDocument();
    expect(screen.getByText('1.5 / 15')).toBeInTheDocument();
    expect(screen.getByText(t('rspamd.history.showing', { shown: 2, total: 3 }))).toBeInTheDocument();
  });

  it('la pestana del historial se puede enlazar por ?tab=', async () => {
    vi.spyOn(mailSecurityApi, 'rspamdStats').mockResolvedValue(STATS);
    vi.spyOn(mailSecurityApi, 'rspamdHistory').mockResolvedValue({ total: 0, truncated: false, rows: [] });
    renderPage('/platform/rspamd?tab=history');
    expect(await screen.findByText(t('rspamd.history.empty'))).toBeInTheDocument();
  });

  it('sin contrasena del controller lo dice y no ofrece nada mas', async () => {
    vi.spyOn(mailSecurityApi, 'rspamdStats').mockRejectedValue(
      new ApiError(503, { code: 'NOT_CONFIGURED', message: 'integración no configurada' }),
    );
    const history = vi.spyOn(mailSecurityApi, 'rspamdHistory').mockResolvedValue(HISTORY);
    renderPage();
    expect(await screen.findByText(t('rspamd.notConfigured'))).toBeInTheDocument();
    expect(screen.queryByRole('tab')).toBeNull();
    expect(screen.getByRole('button', { name: t('common.refresh') })).toBeDisabled();
    expect(history).not.toHaveBeenCalled();
  });
});
