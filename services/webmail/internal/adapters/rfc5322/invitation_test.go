package rfc5322

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/emersion/go-message/mail"
)

// Una invitacion es un multipart/mixed con el texto y el text/calendar (con su METHOD) en una alternativa, y el
// mismo iCalendar adjunto: lo leen Outlook, Gmail, Apple Mail y Thunderbird.
func TestComposeInvitation(t *testing.T) {
	ical := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:x\r\nSUMMARY:Reunión\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	raw, err := Composer{}.ComposeInvitation(domain.InvitationMail{
		From: domain.Address{Name: "Ana Pérez", Email: "ana@empresa.pe"}, To: []domain.Address{{Email: "bea@cliente.pe"}},
		Cc: []domain.Address{{Email: "ana@empresa.pe"}}, Subject: "Invitación: Reunión", Text: "Te invitan.\n",
		Method: "REQUEST", ICal: ical, MessageID: "m1@empresa.pe", Date: time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCRLF(t, raw)
	if head, _, _ := bytes.Cut(raw, []byte("\r\n\r\n")); !isASCII(head) || !bytes.Contains(head, []byte("Subject: =?utf-8?")) {
		t.Fatalf("el asunto con tildes debe ir codificado (RFC 2047): %s", head)
	}
	mr, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if subject, _ := mr.Header.Subject(); subject != "Invitación: Reunión" {
		t.Fatalf("asunto: %q", subject)
	}
	var calendars, attachments int
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(p.Body)
		switch h := p.Header.(type) {
		case *mail.InlineHeader:
			ct, params, _ := h.ContentType()
			if ct == "text/calendar" {
				calendars++
				if params["method"] != "REQUEST" || params["charset"] != "utf-8" || string(body) != ical {
					t.Fatalf("parte text/calendar: %v %q", params, body)
				}
			}
		case *mail.AttachmentHeader:
			name, _ := h.Filename()
			if name != "invite.ics" || string(body) != ical {
				t.Fatalf("adjunto: %q", name)
			}
			attachments++
		}
	}
	if calendars != 1 || attachments != 1 {
		t.Fatalf("partes: %d text/calendar y %d adjuntos", calendars, attachments)
	}
	if _, err := (Composer{}).ComposeInvitation(domain.InvitationMail{Method: "REQUEST", ICal: ical}); err == nil {
		t.Fatal("sin destinatarios no se compone")
	}
	if strings.Contains(string(raw), "\nBcc:") {
		t.Fatal("una invitacion no lleva Bcc")
	}
}

func isASCII(b []byte) bool {
	for _, c := range b {
		if c > 0x7e {
			return false
		}
	}
	return true
}
