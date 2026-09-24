// Package domain es el vocabulario del relay SMTP: la credencial de una empresa, el sobre de un
// mensaje y los motivos por los que se rechaza, cada uno con su clase (temporal o permanente),
// que el adaptador SMTP traduce a codigos 4xx o 5xx sin detalles internos.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// TokenPrefix abre el token de una clave de API, que es la contrasena SMTP.
const TokenPrefix = "cfm_"

// SendScope es el permiso que una clave necesita para enviar por SMTP.
const (
	SendModule   = "transactional"
	SendResource = "messages"
	SendAction   = "create"
)

// Credential es una clave de API ya resuelta que puede enviar. Token es la contrasena de la
// sesion: se conserva en memoria para volver a comprobar la clave en cada mensaje y nunca se
// registra.
type Credential struct {
	KeyID    string
	TenantID string
	Prefix   string
	Token    string
}

// Envelope es el sobre SMTP de un mensaje.
type Envelope struct {
	From       string
	Recipients []string
}

// Kind separa lo que el cliente debe reintentar (4xx) de lo que no (5xx).
type Kind int

const (
	Temporary Kind = iota
	Permanent
)

// Rejection es un rechazo con su clase y un motivo para las metricas (conjunto cerrado).
type Rejection struct {
	Kind   Kind
	Reason string
}

func (r *Rejection) Error() string { return "smtp-relay: " + r.Reason }

func reject(kind Kind, reason string) *Rejection { return &Rejection{Kind: kind, Reason: reason} }

// Motivos de rechazo. Son tambien la etiqueta reason de las metricas.
var (
	ErrAuthRequired      = reject(Permanent, "auth_required")
	ErrAuthFailed        = reject(Permanent, "auth_failed")
	ErrAuthUnavailable   = reject(Temporary, "auth_unavailable")
	ErrBlocked           = reject(Temporary, "blocked")
	ErrConnectionLimit   = reject(Temporary, "connection_rate")
	ErrMessageRate       = reject(Temporary, "message_rate")
	ErrTooManyRecipients = reject(Temporary, "too_many_recipients")
	ErrInvalidAddress    = reject(Permanent, "invalid_address")
	ErrTooLarge          = reject(Permanent, "too_large")
	ErrMalformed         = reject(Permanent, "malformed")
	ErrInfected          = reject(Permanent, "infected")
	ErrScanUnavailable   = reject(Temporary, "scan_unavailable")
	ErrKeyRevoked        = reject(Permanent, "key_revoked")
	ErrSenderNotVerified = reject(Permanent, "sender_not_verified")
	ErrSendingDenied     = reject(Permanent, "sending_denied")
	ErrSendingThrottled  = reject(Temporary, "sending_throttled")
	ErrRejected          = reject(Permanent, "rejected")
	ErrUpstream          = reject(Temporary, "upstream_unavailable")
)

// AsRejection devuelve el rechazo que envuelve err, o uno temporal generico: un fallo que no se
// entiende nunca se da por definitivo.
func AsRejection(err error) *Rejection {
	var r *Rejection
	if errors.As(err, &r) {
		return r
	}
	return ErrUpstream
}

// Inspection es lo que se sabe del mensaje al terminar el DATA.
type Inspection struct {
	HasAttachments bool
	MessageID      string
}

// NormalizeAddress limpia una direccion del sobre y dice si tiene forma de correo. El sobre
// vacio (<>) no se admite: un relay de envio no origina avisos de entrega.
func NormalizeAddress(addr string) (string, bool) {
	addr = strings.TrimSpace(addr)
	at := strings.LastIndex(addr, "@")
	if at <= 0 || at == len(addr)-1 || len(addr) > 320 || strings.ContainsAny(addr, " \t\r\n<>,;\"") {
		return "", false
	}
	local, host := addr[:at], strings.ToLower(addr[at+1:])
	if len(local) > 64 || !strings.Contains(host, ".") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return "", false
	}
	return local + "@" + host, true
}

// IdempotencyKey identifica un mensaje por su empresa, su Message-ID y su sobre: un cliente que
// repite el DATA porque perdio la respuesta conserva el Message-ID y no envia dos veces. Sin
// Message-ID no hay clave: dos mensajes identicos pueden ser dos envios legitimos (un aviso que se
// repite) y deduplicarlos por contenido perderia el segundo.
func IdempotencyKey(tenantID, messageID string, env Envelope) string {
	if strings.TrimSpace(messageID) == "" {
		return ""
	}
	h := sha256.Sum256([]byte(tenantID + "\n" + messageID + "\n" + env.From + "\n" + strings.Join(env.Recipients, ",")))
	return "smtp-" + hex.EncodeToString(h[:])
}
