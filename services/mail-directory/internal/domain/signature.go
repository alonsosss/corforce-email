package domain

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	// MaxSignatureHTMLBytes y MaxSignatureTextBytes acotan lo que se anade a cada mensaje: una firma
	// mas larga es casi siempre una imagen incrustada, que el webmail no admite en la firma.
	MaxSignatureHTMLBytes = 16 * 1024
	MaxSignatureTextBytes = 16 * 1024
)

// MailboxSignature es la firma de un buzon. HTML llega ya saneado por el webmail (HTMLSanitizer),
// que tambien genera Text; el directorio solo la guarda y la devuelve, nunca la interpreta.
type MailboxSignature struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	Username  string
	Enabled   bool
	HTML      string
	Text      string
	OnReplies bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewMailboxSignature es lo que ve un buzon que nunca guardo firma: desactivada y vacia.
func NewMailboxSignature(tenantID uuid.UUID, username string) *MailboxSignature {
	return &MailboxSignature{TenantID: tenantID, Username: username}
}

// Normalize valida la firma. Una firma activa necesita contenido; una desactivada se guarda tal
// cual para que el usuario la recupere al volver a activarla.
func (s *MailboxSignature) Normalize() error {
	s.HTML = strings.TrimSpace(s.HTML)
	s.Text = strings.TrimSpace(strings.ReplaceAll(s.Text, "\r\n", "\n"))
	if err := checkSignaturePart("html", s.HTML, MaxSignatureHTMLBytes); err != nil {
		return err
	}
	if err := checkSignaturePart("text", s.Text, MaxSignatureTextBytes); err != nil {
		return err
	}
	if s.Enabled && s.HTML == "" && s.Text == "" {
		return fieldErr("html", "una firma activa necesita contenido")
	}
	return nil
}

func checkSignaturePart(field, s string, maxBytes int) error {
	if len(s) > maxBytes {
		return fieldErr(field, "supera el tamaño máximo")
	}
	if !utf8.ValidString(s) {
		return fieldErr(field, "no es UTF-8 válido")
	}
	for _, r := range s {
		if r != '\n' && r != '\r' && r != '\t' && unicode.IsControl(r) {
			return fieldErr(field, "contiene caracteres de control")
		}
	}
	return nil
}
