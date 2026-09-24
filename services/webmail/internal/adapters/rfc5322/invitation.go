package rfc5322

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/emersion/go-message/mail"
)

// invitationFilename es el nombre del iCalendar adjunto: los clientes que no leen la parte text/calendar en linea
// (RFC 6047, 2.4) lo abren como fichero.
const invitationFilename = "invite.ics"

// ComposeInvitation arma un correo iMIP: multipart/mixed con una parte multipart/alternative (el texto y el
// iCalendar como text/calendar con su METHOD) y el mismo iCalendar adjunto como application/ics.
func (Composer) ComposeInvitation(m domain.InvitationMail) ([]byte, error) {
	if m.Method == "" || m.ICal == "" || len(m.To)+len(m.Cc) == 0 {
		return nil, errors.New("componer la invitacion: faltan el metodo, el iCalendar o los destinatarios")
	}
	var h mail.Header
	h.SetDate(m.Date)
	h.SetMessageID(m.MessageID)
	h.SetAddressList("From", addresses([]domain.Address{m.From}))
	if len(m.To) > 0 {
		h.SetAddressList("To", addresses(m.To))
	}
	if len(m.Cc) > 0 {
		h.SetAddressList("Cc", addresses(m.Cc))
	}
	h.SetSubject(m.Subject)
	h.Set("MIME-Version", "1.0")

	var buf bytes.Buffer
	mw, err := mail.CreateWriter(&buf, h)
	if err != nil {
		return nil, fmt.Errorf("componer la invitacion: %w", err)
	}
	iw, err := mw.CreateInline()
	if err != nil {
		return nil, fmt.Errorf("componer la invitacion: %w", err)
	}
	calendarType := map[string]string{"charset": "utf-8", "method": m.Method}
	for _, p := range []struct {
		mediaType string
		params    map[string]string
		body      string
	}{{"text/plain", utf8Charset, m.Text}, {"text/calendar", calendarType, m.ICal}} {
		var ph mail.InlineHeader
		ph.SetContentType(p.mediaType, p.params)
		body, err := iw.CreatePart(ph)
		if err != nil {
			return nil, fmt.Errorf("componer la invitacion: %w", err)
		}
		if err := writeAndClose(body, []byte(p.body)); err != nil {
			return nil, fmt.Errorf("componer la invitacion: %w", err)
		}
	}
	if err := iw.Close(); err != nil {
		return nil, fmt.Errorf("componer la invitacion: %w", err)
	}
	var ah mail.AttachmentHeader
	ah.SetContentType("application/ics", map[string]string{"name": invitationFilename})
	ah.SetFilename(invitationFilename)
	body, err := mw.CreateAttachment(ah)
	if err != nil {
		return nil, fmt.Errorf("componer la invitacion: %w", err)
	}
	if err := writeAndClose(body, []byte(m.ICal)); err != nil {
		return nil, fmt.Errorf("componer la invitacion: %w", err)
	}
	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("componer la invitacion: %w", err)
	}
	return buf.Bytes(), nil
}
