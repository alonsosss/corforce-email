import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { webmailApi, type Invitation } from '@/api/webmail';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import { hasInvitation, InvitationCard } from './InvitationCard';

const REQUEST: Invitation = {
  method: 'REQUEST',
  uid: 'u1',
  sequence: 0,
  title: 'Revision de contrato',
  location: 'Sala 2',
  description: '',
  start: '2026-10-01T15:00:00Z',
  end: '2026-10-01T16:00:00Z',
  all_day: false,
  timezone: 'America/Lima',
  recurring: false,
  recurrence_id: null,
  organizer: { email: 'jefe@cliente.pe', name: 'Jefe' },
  attendees: [{ email: 'ana@empresa.pe', name: '', partstat: 'NEEDS-ACTION' }],
  event_id: '',
  attendee: 'ana@empresa.pe',
  partstat: 'NEEDS-ACTION',
  is_organizer: false,
};

function renderCard() {
  render(
    <ToastProvider>
      <MemoryRouter>
        <InvitationCard folder="INBOX" uid={9} />
      </MemoryRouter>
    </ToastProvider>,
  );
}

describe('invitacion en el lector', () => {
  afterEach(() => vi.restoreAllMocks());

  it('reconoce un mensaje con text/calendar o application/ics', () => {
    const part = { part: '2', filename: '', size: 1, content_id: '', inline: false };
    expect(hasInvitation({ attachments: [{ ...part, content_type: 'text/calendar' }] })).toBe(true);
    expect(hasInvitation({ attachments: [{ ...part, content_type: 'application/ICS' }] })).toBe(
      true,
    );
    expect(hasInvitation({ attachments: [{ ...part, content_type: 'application/pdf' }] })).toBe(
      false,
    );
  });

  it('acepta una invitacion y lo refleja', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'invitation')
      .mockResolvedValueOnce(REQUEST)
      .mockResolvedValue({ ...REQUEST, partstat: 'ACCEPTED', event_id: 'ev1' });
    const respond = vi
      .spyOn(webmailApi, 'respondInvitation')
      .mockResolvedValue({ event_id: 'ev1', reply_sent: true });
    renderCard();

    expect(await screen.findByText('Revision de contrato')).toBeInTheDocument();
    expect(screen.getByText('Jefe <jefe@cliente.pe>')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: t('webmail.invitation.accept') }));
    await waitFor(() => expect(respond).toHaveBeenCalledWith('INBOX', 9, 'ACCEPTED'));
    expect(await screen.findByText(t('webmail.invitation.responded'))).toBeInTheDocument();
    expect(await screen.findByText(t('webmail.invitation.inCalendar'))).toBeInTheDocument();
  });

  it('una respuesta a una reunion propia se aplica sola al calendario', async () => {
    vi.spyOn(webmailApi, 'invitation').mockResolvedValue({
      ...REQUEST,
      method: 'REPLY',
      is_organizer: true,
      attendee: '',
      event_id: 'ev1',
      attendees: [{ email: 'bea@cliente.pe', name: 'Bea', partstat: 'DECLINED' }],
    });
    const apply = vi
      .spyOn(webmailApi, 'applyInvitation')
      .mockResolvedValue({ method: 'REPLY', changed: true, event_id: 'ev1' });
    renderCard();

    expect(
      await screen.findByText(
        t('webmail.invitation.replyApplied', {
          attendee: 'Bea',
          status: t('webmail.calendar.partstat.DECLINED'),
        }),
      ),
    ).toBeInTheDocument();
    expect(apply).toHaveBeenCalledTimes(1);
  });

  it('una cancelacion se quita del calendario a peticion y un mensaje sin invitado lo dice', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'invitation').mockResolvedValue({
      ...REQUEST,
      method: 'CANCEL',
      event_id: 'ev1',
    });
    const apply = vi
      .spyOn(webmailApi, 'applyInvitation')
      .mockResolvedValue({ method: 'CANCEL', changed: true, event_id: '' });
    renderCard();

    await user.click(
      await screen.findByRole('button', { name: t('webmail.invitation.cancelApply') }),
    );
    await waitFor(() => expect(apply).toHaveBeenCalledWith('INBOX', 9));
    expect(await screen.findAllByText(t('webmail.invitation.cancelApplied'))).not.toHaveLength(0);
  });

  it('sin estar invitado no ofrece responder', async () => {
    vi.spyOn(webmailApi, 'invitation').mockResolvedValue({
      ...REQUEST,
      attendee: '',
      partstat: '',
    });
    renderCard();
    expect(await screen.findByText(t('webmail.invitation.notInvited'))).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: t('webmail.invitation.accept') }),
    ).not.toBeInTheDocument();
  });
});
