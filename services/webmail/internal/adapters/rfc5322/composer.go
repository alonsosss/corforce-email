// Package rfc5322 compone los mensajes que redacta el webmail con go-message: cabeceras
// codificadas (RFC 2047 y 2231), texto en quoted-printable y adjuntos en base64, con CRLF
// en todas las lineas (Postfix rechaza el LF suelto: smtpd_forbid_bare_newline).
package rfc5322

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	gomessage "github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-message/textproto"
)

// Composer implementa ports.Composer.
type Composer struct{}

func New() Composer { return Composer{} }

var utf8Charset = map[string]string{"charset": "utf-8"}

// Compose arma el mensaje. El Bcc solo se escribe en la copia que se guarda.
func (Composer) Compose(out domain.Outgoing, includeBcc bool) ([]byte, error) {
	var h mail.Header
	h.SetDate(out.Date)
	h.SetMessageID(out.MessageID)
	h.SetAddressList("From", addresses([]domain.Address{out.From}))
	if len(out.To) > 0 {
		h.SetAddressList("To", addresses(out.To))
	}
	if len(out.Cc) > 0 {
		h.SetAddressList("Cc", addresses(out.Cc))
	}
	if includeBcc && len(out.Bcc) > 0 {
		h.SetAddressList("Bcc", addresses(out.Bcc))
	}
	h.SetSubject(out.Subject)
	if out.InReplyTo != "" {
		h.SetMsgIDList("In-Reply-To", []string{out.InReplyTo})
	}
	if len(out.References) > 0 {
		h.SetMsgIDList("References", out.References)
	}
	h.Set("MIME-Version", "1.0")

	var buf bytes.Buffer
	var err error
	switch {
	case len(out.Attachments) == 0 && out.HTML == "":
		err = writeSinglePart(&buf, h, out.Text)
	case len(out.Attachments) == 0:
		err = writeAlternative(&buf, h, out.Text, out.HTML)
	default:
		err = writeMixed(&buf, h, out)
	}
	if err != nil {
		return nil, fmt.Errorf("componer el mensaje: %w", err)
	}
	return buf.Bytes(), nil
}

func addresses(list []domain.Address) []*mail.Address {
	out := make([]*mail.Address, len(list))
	for i, a := range list {
		out[i] = &mail.Address{Name: a.Name, Address: a.Email}
	}
	return out
}

func writeSinglePart(w io.Writer, h mail.Header, text string) error {
	h.SetContentType("text/plain", utf8Charset)
	body, err := mail.CreateSingleInlineWriter(w, h)
	if err != nil {
		return err
	}
	return writeAndClose(body, []byte(text))
}

func writeAlternative(w io.Writer, h mail.Header, text, html string) error {
	iw, err := mail.CreateInlineWriter(w, h)
	if err != nil {
		return err
	}
	if err := writeInlineParts(iw, text, html); err != nil {
		return err
	}
	return iw.Close()
}

func writeMixed(w io.Writer, h mail.Header, out domain.Outgoing) error {
	mw, err := mail.CreateWriter(w, h)
	if err != nil {
		return err
	}
	if out.HTML != "" {
		iw, err := mw.CreateInline()
		if err != nil {
			return err
		}
		if err := writeInlineParts(iw, out.Text, out.HTML); err != nil {
			return err
		}
		if err := iw.Close(); err != nil {
			return err
		}
	} else {
		var th mail.InlineHeader
		th.SetContentType("text/plain", utf8Charset)
		body, err := mw.CreateSingleInline(th)
		if err != nil {
			return err
		}
		if err := writeAndClose(body, []byte(out.Text)); err != nil {
			return err
		}
	}
	for _, a := range out.Attachments {
		var ah mail.AttachmentHeader
		ah.SetContentType(a.ContentType, nil)
		ah.SetFilename(a.Filename)
		body, err := mw.CreateAttachment(ah)
		if err != nil {
			return err
		}
		if err := writeAndClose(body, a.Data); err != nil {
			return err
		}
	}
	return mw.Close()
}

func writeInlineParts(iw *mail.InlineWriter, text, html string) error {
	for _, p := range []struct{ mediaType, body string }{{"text/plain", text}, {"text/html", html}} {
		var ph mail.InlineHeader
		ph.SetContentType(p.mediaType, utf8Charset)
		body, err := iw.CreatePart(ph)
		if err != nil {
			return err
		}
		if err := writeAndClose(body, []byte(p.body)); err != nil {
			return err
		}
	}
	return nil
}

// Finalize prepara un mensaje guardado para salir a su hora: la cabecera Date pasa a ser la del
// envio, la version que viaja pierde el Bcc y el sobre se saca de From, To, Cc y Bcc. El cuerpo no
// se toca: los bytes que salen son los que el usuario programo.
func (Composer) Finalize(stored []byte, date time.Time) (domain.FinalizedMessage, error) {
	br := bufio.NewReader(bytes.NewReader(stored))
	th, err := textproto.ReadHeader(br)
	if err != nil {
		return domain.FinalizedMessage{}, domain.NewValidationError("message", "cabeceras ilegibles")
	}
	body, err := io.ReadAll(br)
	if err != nil {
		return domain.FinalizedMessage{}, domain.NewValidationError("message", "cuerpo ilegible")
	}
	h := mail.Header{Header: gomessage.Header{Header: th}}
	from, err := h.AddressList("From")
	if err != nil || len(from) != 1 {
		return domain.FinalizedMessage{}, domain.NewValidationError("from", "el mensaje debe tener un único remitente")
	}
	sender, err := domain.NewAddress("from", "", from[0].Address)
	if err != nil {
		return domain.FinalizedMessage{}, err
	}
	var recipients []string
	seen := map[string]bool{}
	for _, key := range []string{"To", "Cc", "Bcc"} {
		field := strings.ToLower(key)
		list, err := h.AddressList(key)
		if err != nil {
			return domain.FinalizedMessage{}, domain.NewValidationError(field, "lista de direcciones inválida")
		}
		for _, a := range list {
			addr, err := domain.NewAddress(field, "", a.Address)
			if err != nil {
				return domain.FinalizedMessage{}, err
			}
			if k := strings.ToLower(addr.Email); !seen[k] {
				seen[k] = true
				recipients = append(recipients, addr.Email)
			}
		}
	}
	h.SetDate(date)
	storedCopy, err := withHeader(h.Header.Header, body)
	if err != nil {
		return domain.FinalizedMessage{}, err
	}
	wireHeader := h.Header.Header.Copy()
	wireHeader.Del("Bcc")
	wire, err := withHeader(wireHeader, body)
	if err != nil {
		return domain.FinalizedMessage{}, err
	}
	return domain.FinalizedMessage{From: sender.Email, Recipients: recipients, Wire: wire, Stored: storedCopy}, nil
}

func withHeader(h textproto.Header, body []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := textproto.WriteHeader(&buf, h); err != nil {
		return nil, fmt.Errorf("escribir las cabeceras: %w", err)
	}
	buf.Write(body)
	return buf.Bytes(), nil
}

func writeAndClose(w io.WriteCloser, data []byte) error {
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}
