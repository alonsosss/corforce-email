package ports

import (
	"context"
	"io"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

// Authenticator verifica la credencial de un buzon contra mail-auth.
type Authenticator interface {
	// Verify devuelve la identidad del buzon si la credencial abre el webmail. Todo
	// rechazo es domain.ErrInvalidCredentials, sin distinguir la causa; un fallo del
	// verificador es domain.ErrUnavailable.
	Verify(ctx context.Context, username, password, remoteIP string) (domain.Identity, error)
}

// SessionStore guarda las sesiones del webmail. La clave es el hash del token: el token
// en claro solo existe en la cookie del navegador.
type SessionStore interface {
	Create(ctx context.Context, key string, s domain.Session, ttl time.Duration) error
	// Get devuelve domain.ErrSessionInvalid si la sesion no existe.
	Get(ctx context.Context, key string) (domain.Session, error)
	// Touch renueva la inactividad de una sesion existente.
	Touch(ctx context.Context, key string, ttl time.Duration) error
	Delete(ctx context.Context, key, username string) error
	// Revoke invalida todas las sesiones del buzon abiertas hasta at.
	Revoke(ctx context.Context, username string, at time.Time) error
	// RevokedAt es la ultima revocacion vigente del buzon (cero si no hay).
	RevokedAt(ctx context.Context, username string) (time.Time, error)
}

// MailStore abre el buzon en el servidor IMAP de la celda.
type MailStore interface {
	// Open devuelve una conexion autenticada como el buzon; la cierra quien la abre.
	Open(ctx context.Context, username string) (Mailbox, error)
}

// Mailbox es una conexion IMAP abierta sobre un buzon.
type Mailbox interface {
	Close() error
	// Folders lista las carpetas; withCounts pide totales y no leidos.
	Folders(ctx context.Context, withCounts bool) ([]domain.Folder, error)
	// Quota devuelve nil si el servidor no informa de cuota.
	Quota(ctx context.Context) (*domain.Quota, error)
	List(ctx context.Context, folder string, q domain.ListQuery) (domain.MessagePage, error)
	Read(ctx context.Context, folder string, uid uint32, opts domain.ReadOptions) (*domain.RawMessage, error)
	// OpenPart devuelve la parte decodificada. El lector es valido hasta que se cierra y
	// falla con domain.ErrPartTooLarge si la parte supera maxBytes.
	OpenPart(ctx context.Context, folder string, uid uint32, partID string, maxBytes int64) (domain.Part, io.ReadCloser, error)
	ReplyReference(ctx context.Context, folder string, uid uint32) (domain.ReplyReference, error)
	SetFlags(ctx context.Context, folder string, uid uint32, change domain.FlagChange) error
	Move(ctx context.Context, folder string, uid uint32, dest string) error
	// Expunge borra el mensaje de forma definitiva.
	Expunge(ctx context.Context, folder string, uid uint32) error
	// Append guarda un mensaje y devuelve su UID (0 si el servidor no lo informa).
	Append(ctx context.Context, folder string, raw []byte, flags []domain.Flag, date time.Time) (uint32, error)
}

// Sender entrega un mensaje por el submission de la celda autenticado como el buzon, de
// modo que Postfix aplique smtpd_sender_login_maps al remitente.
type Sender interface {
	Send(ctx context.Context, username, envelopeFrom string, recipients []string, raw []byte) error
}

// SenderDirectory dice con que direcciones concretas puede enviar un buzon segun el
// directorio de la celda, con la misma regla que aplica Postfix (smtpd_sender_login_maps).
// Un fallo es domain.ErrUnavailable.
type SenderDirectory interface {
	SenderIdentities(ctx context.Context, username string) ([]string, error)
}

// SendLedger recuerda cada envio por su clave de idempotencia para que un reintento del
// cliente nunca entregue el mensaje dos veces.
type SendLedger interface {
	// Reserve guarda rec si la clave no tenia registro (reserved true; current lleva el Token
	// nuevo). Si ya lo tenia, lo devuelve sin tocarlo.
	Reserve(ctx context.Context, key string, rec domain.SendRecord, ttl time.Duration) (current domain.SendRecord, reserved bool, err error)
	// Update sobrescribe el registro si sigue siendo de rec.Token; false si ya no lo es.
	Update(ctx context.Context, key string, rec domain.SendRecord, ttl time.Duration) (bool, error)
	// Release borra el registro si sigue siendo de token: el mensaje no salio y la clave
	// queda libre para reintentar.
	Release(ctx context.Context, key, token string) error
}

// Composer arma el mensaje RFC 5322. includeBcc solo para la copia que se guarda: el Bcc
// nunca viaja en el mensaje que se entrega.
type Composer interface {
	Compose(msg domain.Outgoing, includeBcc bool) ([]byte, error)
}

// HTMLSanitizer sanea el HTML de terceros y el que redacta el usuario.
type HTMLSanitizer interface {
	Incoming(html string, opts domain.SanitizeOptions) domain.SanitizedHTML
	// Outgoing devuelve el HTML saneado y su version en texto plano.
	Outgoing(html string) (clean, plain string)
}

// VirusScanner analiza un adjunto antes de guardarlo o enviarlo. Devuelve
// domain.ErrAttachmentInfected o domain.ErrScanUnavailable.
type VirusScanner interface {
	Scan(ctx context.Context, name string, data []byte) error
}

// PartURL construye la URL con la que el cliente pide una parte (imagenes cid:).
type PartURL func(folder string, uid uint32, partID string) string
