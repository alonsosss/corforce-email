package domain

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	// MaxQuickReplies acota las respuestas rapidas de un buzon: el selector de la redaccion las
	// muestra todas.
	MaxQuickReplies = 100
	// MaxQuickReplyNameRunes es lo que cabe en una linea del selector.
	MaxQuickReplyNameRunes = 80
	// MaxQuickReplyHTMLBytes y MaxQuickReplyTextBytes son los de la firma: un texto que se inserta en
	// un mensaje, no un documento.
	MaxQuickReplyHTMLBytes = 16 * 1024
	MaxQuickReplyTextBytes = 16 * 1024
)

// QuickReply es una respuesta rapida del buzon. HTML llega ya saneado por el webmail, que tambien
// genera Text; las variables ({nombre}, {empresa}...) se guardan tal cual y las resuelve la interfaz.
// El directorio solo la guarda y la devuelve, nunca la interpreta.
type QuickReply struct {
	ID        uuid.UUID `json:"id"`
	TenantID  uuid.UUID `json:"tenant_id"`
	Username  string    `json:"username"`
	Name      string    `json:"name"`
	HTML      string    `json:"html"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Normalize valida la respuesta: un nombre de una linea y contenido en al menos una de sus versiones.
func (q *QuickReply) Normalize() error {
	q.Name = strings.Join(strings.Fields(q.Name), " ")
	if !utf8.ValidString(q.Name) || strings.ContainsFunc(q.Name, unicode.IsControl) {
		return fieldErr("name", "tiene caracteres no válidos")
	}
	if q.Name == "" {
		return fieldErr("name", "es obligatorio")
	}
	if utf8.RuneCountInString(q.Name) > MaxQuickReplyNameRunes {
		return fieldErr("name", "es demasiado largo")
	}
	q.HTML = strings.TrimSpace(q.HTML)
	q.Text = strings.TrimSpace(strings.ReplaceAll(q.Text, "\r\n", "\n"))
	if err := checkSignaturePart("html", q.HTML, MaxQuickReplyHTMLBytes); err != nil {
		return err
	}
	if err := checkSignaturePart("text", q.Text, MaxQuickReplyTextBytes); err != nil {
		return err
	}
	if q.HTML == "" && q.Text == "" {
		return fieldErr("html", "la respuesta necesita contenido")
	}
	return nil
}
