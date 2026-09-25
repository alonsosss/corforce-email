package rfc5322

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	gomessage "github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
)

func outgoing() domain.Outgoing {
	return domain.Outgoing{
		Draft: domain.Draft{
			From:    domain.Address{Name: "Ana Pérez", Email: "ana@empresa.pe"},
			To:      []domain.Address{{Name: "Luis", Email: "luis@x.com"}},
			Cc:      []domain.Address{{Email: "copia@x.com"}},
			Bcc:     []domain.Address{{Email: "oculto@x.com"}},
			Subject: "Reunión mañana",
			Text:    "Hola\nlínea 2\n",
		},
		MessageID:  "abc123@empresa.pe",
		Date:       time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC),
		InReplyTo:  "orig@x.com",
		References: []string{"a@x.com", "orig@x.com"},
	}
}

func assertCRLF(t *testing.T, raw []byte) {
	t.Helper()
	for i, b := range raw {
		if b == '\n' && (i == 0 || raw[i-1] != '\r') {
			t.Fatalf("LF suelto en el byte %d: Postfix lo rechaza (smtpd_forbid_bare_newline)", i)
		}
	}
}

func TestComposeCabecerasYBcc(t *testing.T) {
	out := outgoing()
	wire, err := Composer{}.Compose(out, false)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := Composer{}.Compose(out, true)
	if err != nil {
		t.Fatal(err)
	}
	assertCRLF(t, wire)

	mr, err := mail.CreateReader(bytes.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	h := mr.Header
	if s, _ := h.Subject(); s != "Reunión mañana" {
		t.Fatalf("asunto: %q", s)
	}
	if from, _ := h.AddressList("From"); len(from) != 1 || from[0].Name != "Ana Pérez" || from[0].Address != "ana@empresa.pe" {
		t.Fatalf("From: %+v", from)
	}
	if id, _ := h.MessageID(); id != "abc123@empresa.pe" {
		t.Fatalf("Message-ID: %q", id)
	}
	if refs, _ := h.MsgIDList("References"); len(refs) != 2 || refs[1] != "orig@x.com" {
		t.Fatalf("References: %v", refs)
	}
	if irt, _ := h.MsgIDList("In-Reply-To"); len(irt) != 1 || irt[0] != "orig@x.com" {
		t.Fatalf("In-Reply-To: %v", irt)
	}
	if h.Get("Bcc") != "" || bytes.Contains(wire, []byte("oculto@x.com")) {
		t.Fatal("el mensaje que se entrega no revela el Bcc")
	}
	if !bytes.Contains(stored, []byte("oculto@x.com")) {
		t.Fatal("la copia guardada conserva el Bcc")
	}

	p, err := mr.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(p.Body)
	if string(body) != "Hola\r\nlínea 2\r\n" {
		t.Fatalf("cuerpo: %q", body)
	}
}

func TestComposeAlternativaYAdjuntos(t *testing.T) {
	out := outgoing()
	out.HTML = "<p>Hola</p>"
	out.Text = "Hola"
	pdf := []byte("%PDF-1.4 contenido")
	out.Attachments = []domain.Attachment{{Filename: "informe año.pdf", ContentType: "application/pdf", Data: pdf}}
	raw, err := Composer{}.Compose(out, false)
	if err != nil {
		t.Fatal(err)
	}
	assertCRLF(t, raw)

	mr, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var inline []string
	var filename string
	var attached []byte
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch h := p.Header.(type) {
		case *mail.InlineHeader:
			ct, _, _ := h.ContentType()
			inline = append(inline, ct)
		case *mail.AttachmentHeader:
			filename, _ = h.Filename()
			attached, _ = io.ReadAll(p.Body)
		}
	}
	if len(inline) != 2 || inline[0] != "text/plain" || inline[1] != "text/html" {
		t.Fatalf("partes en linea: %v", inline)
	}
	if filename != "informe año.pdf" || !bytes.Equal(attached, pdf) {
		t.Fatalf("adjunto: %q %q", filename, attached)
	}
}

func TestComposeImagenesDelCuerpoEnRelated(t *testing.T) {
	out := outgoing()
	out.Text = "Logo"
	out.HTML = `<p>Logo</p><img src="cid:img1@empresa.com">`
	png := []byte("\x89PNG\r\n\x1a\nresto")
	out.Inline = []domain.InlineImage{{ContentID: "img1@empresa.com", ContentType: "image/png", Data: png}}
	raw, err := Composer{}.Compose(out, false)
	if err != nil {
		t.Fatal(err)
	}
	assertCRLF(t, raw)

	entity, err := gomessage.Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var tree []string
	var cid string
	var data []byte
	err = entity.Walk(func(path []int, e *gomessage.Entity, err error) error {
		if err != nil {
			return err
		}
		ct, _, _ := e.Header.ContentType()
		tree = append(tree, ct)
		if ct == "image/png" {
			cid = e.Header.Get("Content-Id")
			data, _ = io.ReadAll(e.Body)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"multipart/alternative", "text/plain", "multipart/related", "text/html", "image/png"}
	if strings.Join(tree, ",") != strings.Join(want, ",") {
		t.Fatalf("estructura %v, se esperaba %v", tree, want)
	}
	if cid != "<img1@empresa.com>" || !bytes.Equal(data, png) {
		t.Fatalf("imagen: cid %q, %d bytes", cid, len(data))
	}
}
